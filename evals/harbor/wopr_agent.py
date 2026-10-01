"""Harbor installed agent that runs wopr inside a task container.

Usage (from this directory, so the module imports):

    harbor run -d terminal-bench/terminal-bench-2-1@<ref> \
        -a wopr_agent:Wopr -m anthropic/claude-sonnet-5 --ak thinking=high

WOPR_LINUX_BIN_DIR names a directory holding wopr-linux-amd64 and/or
wopr-linux-arm64 (GOOS=linux CGO_ENABLED=0 builds); install() uploads the one
matching the container. Credentials never touch disk in the container: an
Anthropic model gets the host's current OAuth access token through
ANTHROPIC_OAUTH_TOKEN, which wopr uses as-is and never refreshes. The host
refreshes its own login (`wopr auth print-bearer-token --min-expiry`) before
each trial, so containers never hold a refresh token. Other providers take
their usual API-key variables through --ae.
"""

import asyncio
import json
import os
import shlex
import subprocess
from pathlib import Path
from typing import Any, override

from harbor.agents.installed.base import BaseInstalledAgent, with_prompt_template
from harbor.environments.base import BaseEnvironment
from harbor.models.agent.context import AgentContext

_OUTPUT = "wopr.jsonl"
_token_lock = asyncio.Lock()


def _host_wopr() -> str:
    return os.environ.get("WOPR_HOST_BIN", str(Path.home() / ".local/bin/wopr"))


async def _anthropic_token(min_expiry: str) -> str:
    """Return the host's Anthropic access token, refreshed on the host when it
    expires within min_expiry. One refresh at a time, so concurrent trials never
    rotate the refresh token against each other."""
    async with _token_lock:
        out = await asyncio.to_thread(
            subprocess.run,
            [_host_wopr(), "auth", "print-bearer-token", "--provider", "anthropic", "--min-expiry", min_expiry],
            capture_output=True,
            text=True,
            check=True,
        )
    token = out.stdout.strip()
    if not token:
        raise RuntimeError("host wopr printed no Anthropic token")
    return token


class Wopr(BaseInstalledAgent):
    def __init__(self, *args, thinking: str | None = None, min_token_expiry: str = "75m", **kwargs):
        super().__init__(*args, **kwargs)
        self._thinking = thinking
        self._min_token_expiry = min_token_expiry

    @staticmethod
    @override
    def name() -> str:
        return "wopr"

    @override
    def get_version_command(self) -> str | None:
        return "/usr/local/bin/wopr --version"

    @override
    async def install(self, environment: BaseEnvironment) -> None:
        await self.ensure_system_dependencies(environment, ("ca_certificates",))
        arch = (await environment.exec(command="uname -m")).stdout.strip()
        goarch = {"x86_64": "amd64", "amd64": "amd64", "aarch64": "arm64", "arm64": "arm64"}.get(arch)
        if goarch is None:
            raise RuntimeError(f"unsupported container architecture {arch!r}")
        bin_dir = Path(os.environ.get("WOPR_LINUX_BIN_DIR", "."))
        src = bin_dir / f"wopr-linux-{goarch}"
        if not src.exists():
            raise RuntimeError(f"{src} not found; set WOPR_LINUX_BIN_DIR")
        await environment.upload_file(src, "/usr/local/bin/wopr")
        await self.exec_as_root(environment, "chmod 755 /usr/local/bin/wopr")

    @override
    @with_prompt_template
    async def run(self, instruction: str, environment: BaseEnvironment, context: AgentContext) -> None:
        if not self.model_name or "/" not in self.model_name:
            raise ValueError("model must be provider/id")
        env: dict[str, str] = {}
        if self.model_name.startswith("anthropic/"):
            env["ANTHROPIC_OAUTH_TOKEN"] = await _anthropic_token(self._min_token_expiry)
        flags = f"--model {shlex.quote(self.model_name)}"
        if self._thinking:
            flags += f" --thinking {shlex.quote(self._thinking)}"
        # No router.json in the container, so routing is off and the pinned
        # model answers every turn. Print/JSON mode never offers ask_user.
        await self.exec_as_agent(
            environment,
            command=(
                f"/usr/local/bin/wopr -p --mode json {flags} -- {shlex.quote(instruction)} "
                f"</dev/null >/logs/agent/{_OUTPUT} 2>/logs/agent/wopr.stderr"
            ),
            env=env,
        )

    @override
    def populate_context_post_run(self, context: AgentContext) -> None:
        path = self.logs_dir / _OUTPUT
        if not path.exists():
            return
        totals = {"input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0}
        cost = 0.0
        turns = tools = 0
        for line in path.read_text(errors="replace").splitlines():
            try:
                ev = json.loads(line)
            except json.JSONDecodeError:
                continue
            if ev.get("type") == "tool_execution_start":
                tools += 1
            if ev.get("type") != "message_end":
                continue
            msg = ev.get("message") or {}
            if msg.get("role") != "assistant":
                continue
            usage: dict[str, Any] = msg.get("usage") or {}
            turns += 1
            for k in totals:
                totals[k] += int(usage.get(k) or 0)
            cost += float((usage.get("cost") or {}).get("total") or 0)
        context.n_input_tokens = totals["input"] + totals["cacheRead"] + totals["cacheWrite"]
        context.n_cache_tokens = totals["cacheRead"]
        context.n_output_tokens = totals["output"]
        context.cost_usd = cost
        context.metadata = {**(context.metadata or {}), "wopr": {**totals, "turns": turns, "tool_calls": tools}}
