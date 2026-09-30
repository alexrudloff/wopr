# Providers

WOPR speaks to inference providers through its provider registry. Each built-in provider has a Go implementation under `ai/` that handles authentication, request shaping, and stream parsing. To add an OpenAI-compatible endpoint, see [Custom providers](custom-provider.md).

Most hosted providers accept an API key, and some also accept a browser or device sign-in through OAuth. Amazon Bedrock and Google Vertex AI can also use ambient cloud credentials. Run `/login` or `wopr login --list` to see the sign-in methods for each provider.

## Built-in providers

The provider key is the first part of a `provider/model` spec. The wire column lists the APIs that the provider's built-in models use.

| Provider key | Name | Wire | Credential |
|---|---|---|---|
| `amazon-bedrock` | Amazon Bedrock | `bedrock-converse-stream` | AWS credential chain or `AWS_BEARER_TOKEN_BEDROCK`. See [Amazon Bedrock](#amazon-bedrock). |
| `ant-ling` | Ant Ling | `openai-completions` | `ANT_LING_API_KEY` |
| `anthropic` | Anthropic | `anthropic-messages` | `ANTHROPIC_API_KEY`, `ANTHROPIC_OAUTH_TOKEN`, `ANTHROPIC_AUTH_TOKEN`, or OAuth |
| `azure-openai-responses` | Azure OpenAI Responses | `azure-openai-responses` | `AZURE_OPENAI_API_KEY` plus an endpoint. See [Azure OpenAI](#azure-openai). |
| `baseten` | Baseten | `openai-completions` | `BASETEN_API_KEY` |
| `cerebras` | Cerebras | `openai-completions` | `CEREBRAS_API_KEY` |
| `cloudflare-ai-gateway` | Cloudflare AI Gateway | `anthropic-messages`, `openai-completions`, `openai-responses` | `CLOUDFLARE_API_KEY`, `CLOUDFLARE_ACCOUNT_ID`, and `CLOUDFLARE_GATEWAY_ID` |
| `cloudflare-workers-ai` | Cloudflare Workers AI | `openai-completions` | `CLOUDFLARE_API_KEY` and `CLOUDFLARE_ACCOUNT_ID` |
| `deepseek` | DeepSeek | `openai-completions` | `DEEPSEEK_API_KEY` |
| `fireworks` | Fireworks | `anthropic-messages`, `openai-completions` | `FIREWORKS_API_KEY` |
| `github-copilot` | GitHub Copilot | `anthropic-messages`, `openai-completions`, `openai-responses` | OAuth via `wopr login github-copilot`, or `COPILOT_GITHUB_TOKEN` |
| `google` | Google Gemini | `google-generative-ai` | `GEMINI_API_KEY` |
| `google-vertex` | Google Vertex AI | `google-vertex` | `GOOGLE_CLOUD_API_KEY` or Application Default Credentials. See [Google Vertex AI](#google-vertex-ai). |
| `groq` | Groq | `openai-completions` | `GROQ_API_KEY` |
| `huggingface` | Hugging Face | `openai-completions` | `HF_TOKEN` |
| `kimi-coding` | Kimi For Coding | `anthropic-messages` | `KIMI_API_KEY` or OAuth |
| `meta` | Meta | `openai-responses` | `META_API_KEY` or OAuth |
| `minimax` | MiniMax | `anthropic-messages` | `MINIMAX_API_KEY` |
| `minimax-cn` | MiniMax (China) | `anthropic-messages` | `MINIMAX_CN_API_KEY` |
| `mistral` | Mistral | `mistral-conversations` | `MISTRAL_API_KEY` |
| `moonshotai` | Moonshot AI | `openai-completions` | `MOONSHOT_API_KEY` |
| `moonshotai-cn` | Moonshot AI (China) | `openai-completions` | `MOONSHOT_API_KEY` |
| `nvidia` | NVIDIA NIM | `openai-completions` | `NVIDIA_API_KEY` |
| `openai` | OpenAI | `openai-responses` | `OPENAI_API_KEY` |
| `openai-codex` | OpenAI Codex (ChatGPT subscription) | `openai-codex-responses` | OAuth via `wopr login openai-codex` |
| `opencode` | OpenCode Zen | `anthropic-messages`, `google-generative-ai`, `openai-completions`, `openai-responses` | `OPENCODE_API_KEY` |
| `opencode-go` | OpenCode Go | `anthropic-messages`, `openai-completions`, `openai-responses` | `OPENCODE_API_KEY` |
| `openrouter` | OpenRouter | `anthropic-messages`, `openai-completions` | `OPENROUTER_API_KEY` or OAuth |
| `qwen-token-plan` | Qwen Token Plan | `openai-completions` | `QWEN_TOKEN_PLAN_API_KEY` |
| `qwen-token-plan-cn` | Qwen Token Plan (China) | `openai-completions` | `QWEN_TOKEN_PLAN_CN_API_KEY` |
| `qwen-token-plan-individual` | Qwen Token Plan (Individual) | `openai-completions` | `QWEN_TOKEN_PLAN_API_KEY` |
| `together` | Together AI | `openai-completions` | `TOGETHER_API_KEY` |
| `vercel-ai-gateway` | Vercel AI Gateway | `anthropic-messages` | `AI_GATEWAY_API_KEY` |
| `xai` | xAI | `openai-responses` | `XAI_API_KEY` or OAuth |
| `xiaomi` | Xiaomi MiMo | `openai-completions` | `XIAOMI_API_KEY` |
| `xiaomi-token-plan-ams` | Xiaomi MiMo Token Plan (Amsterdam) | `openai-completions` | `XIAOMI_TOKEN_PLAN_AMS_API_KEY` |
| `xiaomi-token-plan-cn` | Xiaomi MiMo Token Plan (China) | `openai-completions` | `XIAOMI_TOKEN_PLAN_CN_API_KEY` |
| `xiaomi-token-plan-sgp` | Xiaomi MiMo Token Plan (Singapore) | `openai-completions` | `XIAOMI_TOKEN_PLAN_SGP_API_KEY` |
| `zai` | ZAI Coding Plan (Global) | `openai-completions` | `ZAI_API_KEY` |
| `zai-coding-cn` | ZAI Coding Plan (China) | `openai-completions` | `ZAI_CODING_CN_API_KEY` |

`wopr --list-models` shows only the providers that have a credential. `wopr --list-models <search>` filters that list.

To add an endpoint that is not in this table, such as a local Ollama, LM Studio or vLLM server, declare it in `~/.wopr/agent/models.json`. See [Custom providers](custom-provider.md).

## Use an API key from the environment

Set the provider's variable before you start WOPR:

```bash
export GEMINI_API_KEY=...
wopr --model google/gemini-2.5-flash
```

Each provider in the table above reads the variable in its credential column. Use `GEMINI_API_KEY` for `google`. WOPR does not treat `GOOGLE_API_KEY` as a `google` credential: `wopr auth check`, `/login` and `--list-models` ignore it.

`anthropic` reads three variables. `ANTHROPIC_AUTH_TOKEN` is sent as an `Authorization: Bearer` token and takes precedence over the other two. `ANTHROPIC_OAUTH_TOKEN` is used as an API key and takes precedence over `ANTHROPIC_API_KEY`. Subscription tokens (`sk-ant-oat`) are sent with the Claude Code identity, and subscription auth shows a warning at session start.

`github-copilot` reads only `COPILOT_GITHUB_TOKEN`. A general `GITHUB_TOKEN` is not a Copilot credential.

`WOPR_CACHE_RETENTION` sets the prompt cache retention that WOPR passes to the provider.

## Authentication

WOPR stores credentials in `~/.wopr/agent/auth.json`. The file is created on demand by `wopr login` and is never read at module init. Environment variables (`OPENAI_API_KEY`, etc.) always take precedence over stored credentials at request time. This lets per-shell or per-project keys override the global file.

`auth.json` can contain API keys and OAuth tokens. Keep it private and do not commit it.

### Load an API key from a command

To use a secret manager without writing the key to disk, set a stored key to a command that starts with `!`:

```json
{
  "anthropic": {
    "type": "api_key",
    "key": "!security find-generic-password -ws 'anthropic'"
  }
}
```

WOPR runs the command when the key is first needed and uses its standard output as the key.

### Provider settings in a credential

A stored API-key credential can include an `env` object. Its values take precedence over the process environment for that provider:

```json
{
  "cloudflare-workers-ai": {
    "type": "api_key",
    "key": "...",
    "env": {
      "CLOUDFLARE_ACCOUNT_ID": "account-id"
    }
  }
}
```

### OpenCode Go

OpenCode Go is a monthly subscription to a set of open models through OpenCode's gateway. In `/setup`, choose **Add an API key → OpenCode Go** (marked "subscription plan") and paste the key from the OpenCode console; there is no sign-in flow. wopr checks the key with a request that runs nothing, lists the plan's models from its live model list, and counts them as subscription models for routing, since the plan bills monthly with 5-hour, weekly, and monthly usage limits rather than per token. Each model is reached in the format OpenCode serves it (Chat Completions, Responses, or Anthropic Messages), and each request carries the conversation's `x-opencode-session` header for OpenCode's routing and prompt caching.

### OAuth providers

Built-in OAuth targets are `anthropic`, `github-copilot`, `kimi-coding`, `meta`, `openai-codex`, `openrouter`, and `xai`. Each provider owns its flow. For example, GitHub Copilot uses device authorization, while callback-based providers can open a localhost callback server. Tokens are persisted to `auth.json` unless the provider owns another store, and supported providers refresh them when required.

A failed Copilot refresh reports an explicit reauthentication instruction:

```
github-copilot: refresh failed - run 'wopr login' to re-authenticate
```

### `wopr login` shapes

```
wopr login                 # interactive picker
wopr login <provider>      # specific provider
wopr login --list          # list the targets without reading credentials
wopr logout <provider>     # remove credentials for one provider
```

`wopr logout` does not unset environment variables or revoke the credential at the provider.

### Credential commands

`wopr auth` resolves credentials the way a model request does and writes them for external clients. Each command needs `--provider <provider>`, `--model <model>`, or both.

```
wopr auth check --provider openai --json      # ready / not_ready / invalid, exit 0 / 1 / 2
wopr auth print-api-key --provider openai     # API key on stdout
wopr auth print-bearer-token --provider openai-codex --min-expiry 1h
```

Credential-printing commands write secrets to stdout. `auth check` prints a credential only with `--credentials`.

Use `wopr auth check` to confirm that WOPR sees a credential before you start a session.

An OAuth login can wait for device authorization or a browser callback. This wait belongs to the provider flow. WOPR does not start a model Session while `wopr login` runs.

## Cloud providers

The providers below need more than one setting, or can use credentials from their cloud platform.

### Azure OpenAI

Set an API key and either a base URL or a resource name:

```bash
export AZURE_OPENAI_API_KEY=...
export AZURE_OPENAI_BASE_URL=https://your-resource.ai.azure.com
# Or:
export AZURE_OPENAI_RESOURCE_NAME=your-resource
```

| Variable | Effect |
|---|---|
| `AZURE_OPENAI_API_KEY` | API key. |
| `AZURE_OPENAI_BASE_URL` | Azure OpenAI or Foundry endpoint. `.openai.azure.com`, `.cognitiveservices.azure.com`, and `.ai.azure.com` hosts are normalized to `/openai/v1`. |
| `AZURE_OPENAI_RESOURCE_NAME` | Alternative to `AZURE_OPENAI_BASE_URL`; builds `https://<resource>.openai.azure.com/openai/v1`. |
| `AZURE_OPENAI_API_VERSION` | API version. Default `v1`. |
| `AZURE_OPENAI_DEPLOYMENT_NAME_MAP` | Optional comma-separated `model=deployment` map for Azure deployments. |

### Amazon Bedrock

Bedrock uses a bearer token or the standard AWS SDK credential chain:

```bash
# Named profile
export AWS_PROFILE=your-profile

# IAM keys
export AWS_ACCESS_KEY_ID=AKIA...
export AWS_SECRET_ACCESS_KEY=...
export AWS_SESSION_TOKEN=...   # for temporary credentials

# Bedrock bearer token
export AWS_BEARER_TOKEN_BEDROCK=...

# Region, when the profile or SDK configuration does not supply one
export AWS_REGION=us-west-2    # AWS_DEFAULT_REGION also works
```

ECS task credentials and IRSA work through the standard `AWS_CONTAINER_CREDENTIALS_*` and `AWS_WEB_IDENTITY_TOKEN_FILE` variables.

### Cloudflare AI Gateway

The gateway needs a token, an account ID, and a gateway ID:

```bash
export CLOUDFLARE_API_KEY=...
export CLOUDFLARE_ACCOUNT_ID=...
export CLOUDFLARE_GATEWAY_ID=...
```

The account and gateway IDs can come from the process environment or from the credential's `env` object in `auth.json`.

### Cloudflare Workers AI

Workers AI needs a token and an account ID:

```bash
export CLOUDFLARE_API_KEY=...
export CLOUDFLARE_ACCOUNT_ID=...
```

### Google Vertex AI

Use a Google Cloud API key:

```bash
export GOOGLE_CLOUD_API_KEY=...
```

To use Application Default Credentials instead, set a project and a location, then sign in:

```bash
export GOOGLE_CLOUD_PROJECT=your-project   # GCLOUD_PROJECT also works
export GOOGLE_CLOUD_LOCATION=us-central1
gcloud auth application-default login
```

To use a service-account key file, set `GOOGLE_APPLICATION_CREDENTIALS` with the project and location.

## Provider resolution

When you select a model through `--model`, `/model`, or F2,
WOPR parses `provider/model`. Always use provider-qualified model specs from
helpers; bare IDs default to `openai` and can route incorrectly.

For Copilot models the model ID itself may contain a slash (e.g. `github-copilot/openai/gpt-5.5`); wopr's `generatedModelSpec` helper preserves trailing slashes when rebuilding specs from generated model objects.

## Troubleshooting

| Symptom | Likely cause | Fix |
|---|---|---|
| `Missing bearer or basic authentication` | No API key in env, no token in `auth.json` | `wopr login <provider>` or export the env var. |
| `wopr auth check --provider google` prints `not_ready` | Only `GOOGLE_API_KEY` is set | Set `GEMINI_API_KEY`. |
| `Bad credentials` (Copilot 401) | OAuth token expired or revoked | `wopr login github-copilot`. |
| Model selector shows nothing | No providers have valid auth | Login or set an env var; check `/login`. |
| Cycling lands on the wrong model | Bare ID in a custom helper | Always pass `provider/model`; see [Models](models.md). |
