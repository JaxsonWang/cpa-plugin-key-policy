# cpa-key-policy

WebSocket-compatible downstream API-key policy plugin for [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI).

It issues plugin-owned `cpa_…` keys and enforces an exact model allow-list, RPM, daily/weekly USD budgets, and token/per-call accounting without overriding CPA model resolution.

| | |
|---|---|
| **Repository** | [JaxsonWang/cpa-plugin-key-policy](https://github.com/JaxsonWang/cpa-plugin-key-policy) |
| **License** | MIT |
| **中文说明** | [README.zh-CN.md](./README.zh-CN.md) |

## v0.5.0 behavior

The policy invariant is:

> The plugin never routes or rewrites a model. Clients send a real model name that CPA can resolve natively, and the plugin only decides whether that exact name is allowed.

This keeps the native Responses WebSocket path available. The plugin registers only:

- frontend authentication;
- request interception;
- usage accounting;
- management API and embedded UI.

It does **not** register `model.route`, a scheduler, or a response interceptor.

### Preserved

- plugin-owned `cpa_…` key authentication;
- exact, case-insensitive model allow-lists;
- per-key RPM;
- daily and rolling-weekly USD limits;
- token pricing and fixed per-call pricing;
- per-key/per-model usage and cost totals;
- key create, edit, rotate, revoke, RPM reset, and usage UI;
- binary `allow_models_endpoint` policy for `GET /v1/models`.

### Removed

- aliases and model-name rewriting;
- provider pinning;
- multi-target priority/round-robin routing;
- credential group/tier scheduling;
- custom credential classification;
- plugin-side catalog aggregation.

Provider names shown in the model picker are discovery-only labels from CPA. They are never persisted into key policy and do not influence routing.

## Why this fixes Responses WebSocket fallback

The previous router hook returned a handled model override. CPA then disabled native WebSocket passthrough for that request. Incremental Responses frames such as `previous_response_id` / `response.append` could require HTTP replay, producing:

```text
1012 upstream requires HTTP replay
```

v0.5.0 leaves model resolution to CPA:

1. During the HTTP WebSocket Upgrade, frontend auth validates that the `cpa_…` key exists and is enabled. It does not consume RPM because the Upgrade has no model execution.
2. Before every WebSocket execution frame, `request.intercept_before` checks the resolved requested model, RPM, and budget using the original authorization header.
3. Normal HTTP requests continue to receive the same policy checks in frontend auth, without double-counting in the interceptor.
4. Unknown/native CPA keys are left to other CPA auth providers.

## Configuration

CLIProxyAPI plugin configuration:

```yaml
plugins:
  enabled: true
  dir: "plugins"
  configs:
    cpa-key-policy:
      enabled: true
      priority: 10
      state_file: "cpa-key-policy-state.json"
```

Canonical key policy shape:

```yaml
enabled: true
state_file: ./cpa-key-policy-state.json

keys:
  - id: team-a
    name: Team A
    enabled: true
    key_hash: "sha256:REPLACE_WITH_SHA256_HEX"
    key_preview: "cpa_..."
    rpm: 60
    daily_limit_usd: 10
    weekly_limit_usd: 50
    allow_models_endpoint: false
    models:
      - model: gpt-5.4
        billing_mode: tokens
        input_price_per_million: 2
        output_price_per_million: 8
        cache_read_price_per_million: 0.2
```

Use the Web UI or Management API to generate keys when possible. The plaintext key is returned once. Do not also add plugin-issued keys to CPA's native `api-keys`, because that creates a separate native authentication path.

If the state file already exists, its keys and usage are loaded as the runtime source of truth. See [`config.example.yaml`](./config.example.yaml) for the seed format.

## Upgrade from v0.4.x

The v0.5.0 state format is version `2`. Existing state/config is migrated on load:

- a legacy direct rule with `target_model` becomes `{model: target_model}`;
- a legacy global alias contributes each target's real model as a direct rule;
- multiple targets become multiple allowed real models;
- duplicate real models are collapsed;
- alias/provider/group routing metadata is removed;
- pricing is preserved;
- historical `usage.by_alias` rows remain as residual history because an old alias cannot be truthfully assigned to one target model.

After upgrading:

- clients must send real CPA model names; old alias request names no longer resolve;
- back up the state file before the first v0.5.0 load if rollback is required;
- start a new Codex session or restart the client, because an existing session may have cached the SSE fallback decision.

## Web Management UI

After the plugin loads:

```text
http://HOST:PORT/v0/resource/plugins/cpa-key-policy/index.html
```

Log in with the CPA management secret. The UI provides key management, direct model selection, price editing, budget/RPM policy, and per-model usage. The management secret remains in memory rather than `localStorage`.

Development:

```bash
cd web
npm ci
VITE_CPA_BASE=http://127.0.0.1:8317 npm run dev
```

## Management API

Exact plugin paths under `/v0/management/plugins/cpa-key-policy`:

- `GET/POST/PATCH/DELETE /keys`;
- `POST /keys/rotate`;
- `POST /keys/reset-rpm`;
- `GET /keys/usage`;
- `GET /status`.

Create a key; `plain_key` is returned once:

```bash
curl -X POST "$CPA/v0/management/plugins/cpa-key-policy/keys" \
  -H "Authorization: Bearer $MANAGEMENT_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "id": "team-a",
    "name": "Team A",
    "rpm": 60,
    "daily_limit_usd": 10,
    "weekly_limit_usd": 50,
    "models": [
      {
        "model": "gpt-5.4",
        "billing_mode": "tokens",
        "input_price_per_million": 2,
        "output_price_per_million": 8,
        "cache_read_price_per_million": 0.2
      }
    ]
  }'
```

## Build and test

```bash
go test ./...

gofmt -w internal/plugin internal/policy
go vet ./...
go test -race ./internal/policy ./internal/plugin

cd web
npm ci
npm test
npm run typecheck
npm run build
```

Build the embedded UI and Linux shared libraries:

```bash
make web-build
make build-linux-amd64
# or: make build-linux
```

## WebSocket acceptance

1. Install the rebuilt plugin and restart CPA.
2. Start a fresh Codex session with `Authorization: Bearer cpa_…` and a real model name such as `gpt-5.4`.
3. Complete at least two turns so the client exercises incremental Responses state (`previous_response_id` / `response.append`).
4. Confirm RPM/usage increments per execution rather than per Upgrade.
5. Confirm CPA logs do not contain `1012 upstream requires HTTP replay` for the new session.
