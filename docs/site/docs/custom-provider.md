# Custom providers

Add a model provider with `~/.wopr/agent/models.json` when it speaks an
OpenAI-compatible API with static models.

## Choose the smallest mechanism

| Need | Use |
|---|---|
| Add an OpenAI-compatible endpoint and static models | `~/.wopr/agent/models.json` |

See [Models](models.md) before you add a provider.

## Images

A model's `input` lists what it accepts. Without it, a models.json model is
text-only: pasted images and screenshots from `read` reach it as the line
`image content omitted because this model does not accept images`. Add
`"image"` for a vision model:

```json
{
  "providers": {
    "local": {
      "baseUrl": "http://127.0.0.1:8080/v1",
      "api": "openai-completions",
      "models": [{ "id": "qwen-vl", "input": ["text", "image"] }]
    }
  }
}
```

`modelOverrides` can set `input` on a model wopr already knows.

## Security

A custom provider endpoint receives prompts, model responses, headers, and credentials. Treat its configuration as security-sensitive.

Do not:

- hardcode credentials;
- send WOPR or user data to an undeclared endpoint;
- hide certificate failures;
- retry non-idempotent requests without a defined policy;
- retain tokens after logout;
- claim an upstream provider identity for a different service.

## Related documentation

- [Models](models.md)
- [Providers](providers.md)
