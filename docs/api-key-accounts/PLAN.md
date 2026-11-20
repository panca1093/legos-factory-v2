# API-key Claude accounts + account in-use state

## Why

A Claude account pointed at an Anthropic-compatible provider (DeepSeek, GLM) is set up by hand-editing its
`settings.json`, and the factory cannot tell which model a reply really came from.

Root bug, verified by spike on claude-agent-acp: every factory session passes `settingSources` of `[]` (read-only
roles, repo chat) or `["project","local"]` (Builder), so the account's `settings.json` is never read. Base URL and
key still reach the provider because the factory lifts them into the process env, but
`ANTHROPIC_DEFAULT_{OPUS,SONNET,HAIKU}_MODEL` are not lifted. The aliases then resolve to Claude ids
(`claude-opus-5-5`) and are sent to DeepSeek as-is. With the three model vars in the process env, `opus` / `opus[1m]` /
`default` → map.opus, `sonnet` / `claude-sonnet-5-5` → map.sonnet, `haiku` → map.haiku (all verified).

## Decisions (user, 2026-11-20)

- A Claude account can be an **API-key account**, created and edited from the Settings UI. Presets:
  DeepSeek, GLM (Z.ai international, `https://api.z.ai/api/anthropic`), Custom.
- The **key is sealed in the DB** with the existing SecretBox (`secret.key`) and injected into the process env per run.
  It is never written to `settings.json`, never logged, and never returned by the API.
- The existing hand-made DeepSeek account is migrated once: at startup its `settings.json` base URL, key and model map
  move to the DB, and the key fields are removed from the file.
- The model picker and the chat footer show the provider's model name (e.g. `deepseek-flash`), not `claude/opus`.
- Every account that serves at least one role shows **In use**. An account with no role is dimmed and shows **Idle**.
- Out of scope: per-account usage split and provider pricing (later), Gemini agent (separate PR).

## API contract (both slices build against this)

`AccountView` gains:

```json
"api": {
  "provider": "deepseek|glm|custom",
  "base_url": "https://api.deepseek.com/anthropic",
  "host": "api.deepseek.com",
  "models": {"opus": "deepseek-flash[1m]", "sonnet": "deepseek-v4-pro", "haiku": "deepseek-chat"},
  "has_key": true
}
```

`api` is omitted for login accounts. Its status is `signed_in` with plan `API key · <host>`, as today.

- `POST /api/v1/agents/claude/accounts` takes `{label, api?: {provider, base_url, api_key, models}}`.
  With `api` it returns 201 `NewAccount` with empty `login_command` / `env_hint`.
  `api` on any other agent → 400 `INVALID_REQUEST`.
  Validation (400): base_url is an absolute https URL (http only for localhost); api_key is non-empty on create; each
  models value is ≤ 100 chars with no whitespace; at least `opus` or `sonnet` is set.
  Without a sealer configured → 400 with a message saying the factory has no secret key.
- `PUT /api/v1/agents/claude/accounts/:aid/api` takes `{provider, base_url, api_key, models}` → 200 `AccountView`.
  An empty `api_key` keeps the stored key. A login account can be converted. Requires the engineer role, like the other
  account writes.
- `ChatModel.name` for a mapped account is the mapped model name. The model list comes from the **Chat role's
  account**, not from the user's active account.
- Chat message `usage` gains `served_model` (the mapped name, e.g. `deepseek-flash`) and `provider_host`, both
  omitempty. The footer shows `served_model` when present, else `model`.

## Model list for a mapped account

Keep claude-agent-acp's listed values, so `setModel` still matches them, and fold each value into a family:
`default` / `opus*` → opus, `*sonnet*` → sonnet, `*haiku*` → haiku. Keep the first value of each family whose map
entry is set. Drop other values (e.g. `fable`); there is nothing to map them to. Name = the mapped model.

## Verify

- Go unit tests: env carries the base URL, key and three model vars for an API account; blank `ANTHROPIC_API_KEY`;
  key absent from AccountView JSON, errors and logs; migration is idempotent and strips the key from settings.json;
  model folding; PUT keeps the key on empty input.
- UI unit tests: preset fills fields; edit keeps the key when blank; In use / Idle states.
- Live check: chat on the DeepSeek account → transcript records `deepseek-flash`, footer shows `deepseek-flash`.
