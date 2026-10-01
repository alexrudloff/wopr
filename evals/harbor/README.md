# wopr on Harbor

`wopr_agent.py` is a [Harbor](https://harborframework.com) installed agent, so wopr can run Terminal-Bench and other Harbor datasets under the same task images, timeouts, and verifiers as published leaderboard entries.

```sh
# Linux builds of the current tree
for a in amd64 arm64; do CGO_ENABLED=0 GOOS=linux GOARCH=$a go build -o /tmp/woprbin/wopr-linux-$a ./cmd/wopr; done

cd evals/harbor
WOPR_LINUX_BIN_DIR=/tmp/woprbin PYTHONPATH=. harbor run \
  -d terminal-bench/terminal-bench-2-1 -a wopr_agent:Wopr \
  -m anthropic/claude-sonnet-5 --ak thinking=high -k 3 -n 4 -e docker
```

The container gets no `~/.wopr`, so routing is off and the pinned model answers every turn with wopr's default settings. For an Anthropic model the agent asks the host's `wopr` for the current OAuth access token (`wopr auth print-bearer-token --min-expiry 75m`, which refreshes the host login when it is close to expiring) and passes it as `ANTHROPIC_OAUTH_TOKEN`; no refresh token or `auth.json` enters a container. Other providers take their API-key variables through `--ae`.

Each trial's `agent/wopr.jsonl` holds wopr's JSON event stream; token counts and cost come from its assistant `message_end` events. Agent options: `thinking` (`--ak thinking=high`) and `min_token_expiry`.
