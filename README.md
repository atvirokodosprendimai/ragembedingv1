# ragembedingv1

An authenticating, rate-limiting, usage-metering proxy in front of an Ollama
embeddings pool. Clients speak the OpenAI `/v1/embeddings` API with a per-token
API key; the gateway enforces per-key limits, forwards accepted requests to a
Caddy load balancer that fans out across 10 Ollama backends, and records the
`bge-m3` token usage Ollama reports.

```
client ──Bearer sk-rag-…──▶ gateway ──▶ Caddy (:11435) ──▶ ollama-1 … ollama-10
                             │  auth · batch≤N · N/min · token budget · accounting
                             └──▶ dashboard (usage by day/week/month/…)
```

## What it does

- **Per-token API keys** — SQLite-backed (pure Go, **no cgo**), only the SHA-256
  hash is stored. Keys are issued from the CLI, never self-service.
- **Batch limit** — rejects an `/v1/embeddings` request whose `input` array
  exceeds the key's `batch_max` (default 25) with `400`.
- **Rate limit** — per-key requests/minute (default 400). Over the limit returns
  `429` with a `Retry-After` header telling the client exactly how long to wait.
- **Token budget** — each key has a budget of `bge-m3` input tokens:
  - `-1` = unlimited (on-demand), or a prepaid allowance (e.g. `100000000`);
  - scoped **monthly** (resets each calendar month) or **lifetime** (cumulative).
  - An exhausted budget returns `402 Payment Required`.
- **Token accounting** — the authoritative token count comes from Ollama's
  `usage.prompt_tokens`, recorded per key per request.
- **Usage dashboard** — a datastar/templ terminal-style dashboard showing each
  key's tokens across **today, this week, this month, past month, and earlier**,
  plus prepaid budget consumption.

## Status-code contract

| Status | Meaning | Client action |
|--------|---------|---------------|
| `200`  | Forwarded; upstream response relayed verbatim | — |
| `400`  | Bad JSON, or batch size over the key's limit | Fix the request |
| `401`  | Missing / invalid / revoked API key | Check the key |
| `402`  | Token budget exhausted | Wait for monthly reset or top up |
| `429`  | Rate limit exceeded | Retry after `Retry-After` seconds |
| `502`  | Upstream (Ollama/Caddy) unavailable | Retry later |

Errors use the OpenAI error envelope (`{"error":{"message","type"}}`) so existing
OpenAI SDK clients surface them normally.

## Layout

```
cmd/gateway        HTTP gateway (composition root)
cmd/ragctl         operator CLI: create/list/revoke keys
internal/config    .env + env configuration
internal/domain/   apikey + usage (pure business rules)
internal/ratelimit per-token fixed-window limiter
internal/budget    prepaid-allowance checker
internal/proxy     /v1/embeddings enforcement pipeline + forwarder
internal/httpapi   chi router + Bearer-auth middleware
internal/web       datastar/templ usage dashboard
internal/platform/database  GORM + no-cgo SQLite + repositories
migrations/        goose SQL migrations (embedded)
caddy/Caddyfile    load balancer for the 10 Ollama backends
```

## Configuration

Copy `.env.example` to `.env`. Real environment variables override `.env`.

| Variable | Default | Purpose |
|----------|---------|---------|
| `LISTEN_ADDR` | `:8080` | Gateway bind address |
| `DB_PATH` | `ragembed.db` | SQLite file (keys + usage) |
| `CADDY_UPSTREAM_URL` | `http://localhost:11435` | The Caddy load balancer |
| `EMBED_MODEL` | `bge-m3` | Model name recorded with usage |
| `DEFAULT_BATCH_MAX` | `25` | Default inputs/request per key |
| `DEFAULT_RATE_PER_MIN` | `400` | Default requests/min per key |
| `DEFAULT_TOKEN_BUDGET` | `-1` | Default token budget (`-1` = unlimited) |
| `DEFAULT_BUDGET_PERIOD` | `lifetime` | Default period (`monthly`/`lifetime`) |

## Running

```bash
# 1. Edit caddy/Caddyfile with your 10 Ollama backends, then start Caddy:
caddy run --config caddy/Caddyfile

# 2. Start the gateway (creates + migrates the DB on first run):
go run ./cmd/gateway

# 3. Issue a key (limits default from .env; the key is printed once):
go run ./cmd/ragctl create --name my-app
go run ./cmd/ragctl create --name batch --budget 100000000 --period monthly --batch 25 --rate 400

# 4. Use it exactly like the OpenAI embeddings API:
curl http://localhost:8080/v1/embeddings \
  -H "Authorization: Bearer sk-rag-…" \
  -H "Content-Type: application/json" \
  -d '{"model":"bge-m3","input":["hello","world"]}'

# 5. Open the usage dashboard:
open http://localhost:8080/
```

`ragctl list` shows every key with its limits and month/lifetime token usage;
`ragctl revoke --id <n>` disables one.

## Development

```bash
go test ./...            # unit + integration tests
go vet ./...
templ generate           # after editing internal/web/*.templ (never edit *_templ.go)
```

Migrations are goose SQL files under `migrations/`, embedded into the binary and
applied automatically at startup.
