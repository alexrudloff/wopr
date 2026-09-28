# Custom providers

Add a model provider with `~/.wopr/agent/models.json` when it speaks an
OpenAI-compatible API with static models.

## Choose the smallest mechanism

| Need | Use |
|---|---|
| Add an OpenAI-compatible endpoint and static models | `~/.wopr/agent/models.json` |

See [Models](models.md) before you add a provider.

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
