# ragembedingv1

An authenticating, rate-limiting, usage-metering proxy in front of an Ollama
embeddings pool. Clients speak the OpenAI `/v1/embeddings` API with a per-token
API key; the gateway enforces per-key limits, forwards accepted requests to a
Caddy load balancer that fans out across 10 Ollama backends, and records the
`bge-m3` token usage Ollama reports.

```
client ──Bearer sk-rag-…──▶ gateway ──▶ Caddy (:11435) ──▶ ollama-1 … ollama-10
                             │  auth · batch≤N · N/min · token budget · queue · accounting
                             └──▶ dashboard (live pool pressure + usage by day/week/…)
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
- **Priority queue** — the pool has a finite number of concurrent slots
  (`UPSTREAM_MAX_CONCURRENT`), and requests queue for them by key priority
  (`0` = free … `9` = front-of-house). The resources stay open to everyone; when
  they are contended, the operator's own site goes first. See below.
- **Token accounting** — the authoritative token count comes from Ollama's
  `usage.prompt_tokens`, recorded per key per request.
- **Usage dashboard** — a datastar/templ terminal-style dashboard with a live
  pool-pressure strip (slots busy, who is queued, who is being promoted) and each
  key's tokens across **today, this week, this month, past month, and earlier**,
  plus prepaid budget consumption.

## The priority queue

Everything above the queue is a local check, so an unauthenticated, over-limit or
out-of-budget request never occupies a slot. A legitimate request then waits its
turn under two rules:

1. **Strict priority** — the highest-ranked waiter goes first, FIFO within a
   rank. The main site's key never queues behind a batch client's flood.
2. **Anti-starvation** — anyone queued longer than `QUEUE_PROMOTE_AFTER` (5s) is
   admitted ahead of rank, oldest first, so free traffic always drains.
   Promotions **alternate** with priority admissions: under saturation aged
   traffic takes every other slot and priority traffic the rest.

That second half matters. Promoting *every* aged waiter sounds fairer, but with a
backlog deeper than the promotion window everything ages out at once and the
queue collapses back into FIFO — measured against a real Ollama pool, the
priority key's latency went up ~7x. Alternating bounds the priority wait at one
extra admission however deep the backlog.

A released slot is handed straight to the chosen waiter, so a new arrival can
never barge past the queue. Waiting itself is unbounded on purpose: a queued
request waits until a slot frees or its own client hangs up. Every waiter is an
in-flight HTTP request, so the queue is bounded by the connection count, and a
client that gives up removes itself.

```bash
ragctl create --name main-site --priority 9   # front-of-house
ragctl create --name nightly-batch            # priority 0, the default
ragctl priority --id 3 --priority 9           # promote a key already deployed
```

## Dashboard access

The dashboard is operator-only — it lists every key, its limits and its spend —
so it sits behind HTTP Basic auth and **fails closed**: with no
`DASHBOARD_PASSWORD` set it is not served at all (`404`), and the embeddings API
carries on regardless. The whole surface is guarded (`/`, `/keys/{id}`, `/queue`
and the assets); only `/healthz` stays public, so load balancers still work.

```bash
DASHBOARD_USER=admin DASHBOARD_PASSWORD='…' go run ./cmd/gateway
```

Two caveats worth knowing:

- **Basic auth needs TLS.** The credential is base64-encoded on every request,
  not encrypted. If the gateway is reachable from anywhere but localhost, put a
  TLS terminator in front of it.
- **It is one shared credential**, not user accounts — rotate it by changing the
  env var and restarting. Client authentication is separate and unchanged: API
  keys, `Authorization: Bearer sk-rag-…`.

## Status-code contract

| Status | Meaning | Client action |
|--------|---------|---------------|
| `200`  | Forwarded; upstream response relayed verbatim | — |
| `400`  | Bad JSON, or batch size over the key's limit | Fix the request |
| `401`  | Missing / invalid / revoked API key | Check the key |
| `402`  | Token budget exhausted | Wait for monthly reset or top up |
| `429`  | Rate limit exceeded | Retry after `Retry-After` seconds |
| `502`  | Upstream (Ollama/Caddy) unavailable | Retry later |
| `503`  | Cancelled while queued (client hung up, or gateway shutting down) | Retry |

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
internal/queue     priority admission queue in front of the pool
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
| `DEFAULT_PRIORITY` | `0` | Default queue rank for a new key (`0`–`9`) |
| `DASHBOARD_USER` | `admin` | Dashboard Basic-auth username |
| `DASHBOARD_PASSWORD` | *(empty)* | Dashboard Basic-auth password; **empty disables the dashboard** |
| `UPSTREAM_MAX_CONCURRENT` | `10` | Concurrent upstream slots (one per Ollama backend) |
| `QUEUE_PROMOTE_AFTER` | `5s` | Wait after which a queued request is promoted |

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

`ragctl list` shows every key with its limits, queue rank and month/lifetime token
usage; `ragctl priority --id <n> --priority <0-9>` re-ranks one and
`ragctl revoke --id <n>` disables one.

## Development

```bash
go test ./...            # unit + integration tests
go vet ./...
templ generate           # after editing internal/web/*.templ (never edit *_templ.go)
```

Migrations are goose SQL files under `migrations/`, embedded into the binary and
applied automatically at startup.
