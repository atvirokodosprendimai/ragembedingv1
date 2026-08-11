# ragembedingv1

An authenticating, rate-limiting, usage-metering proxy in front of an Ollama
embeddings pool. Clients speak either the OpenAI `/v1/embeddings` API or Ollama's
native `/api/embed`, with a per-token API key; the gateway enforces per-key
limits, forwards accepted requests to a Caddy load balancer that fans out across
10 Ollama backends, and records the `bge-m3` token usage Ollama reports.

```
client ──Bearer sk-rag-…──▶ gateway ──▶ Caddy (:11435) ──▶ ollama-1 … ollama-10
                             │  auth · batch≤N · N/min · token budget · queue · accounting
                             └──▶ dashboard (live pool pressure + usage by day/week/…)
```

## What it does

- **Two endpoints, one pipeline** — `POST /v1/embeddings` (OpenAI-compatible,
  for SDK clients) and `POST /api/embed` (Ollama native). Both take the same
  polymorphic `input` (a string, or an array for a batch) and go through the
  same auth, batch, rate, budget, queue and accounting path. They differ only in
  how the upstream reports tokens — `usage.prompt_tokens` vs
  `prompt_eval_count` — and both are billed identically. Note the old
  single-prompt `/api/embeddings` (plural) is **not** proxied: it takes one
  `prompt`, not a batch.
- **Per-token API keys** — SQLite-backed (pure Go, **no cgo**), only the SHA-256
  hash is stored. Keys are issued from the CLI, never self-service.
- **Batch limit** — rejects a request whose `input` array exceeds the key's
  `batch_max` (default 25) with `400`.
- **Rate limit** — per-key requests/minute (default 100). Over the limit returns
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
- **Live usage dashboard** — a datastar/templ terminal-style dashboard. Each key
  is its own page, and one SSE connection per page streams a live pool-pressure
  strip plus that key's usage the moment a call is recorded. See below.

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
ragctl update --id 3 --priority 9             # promote a key already deployed
```

## The live dashboard (CQRS)

Usage is CQRS-shaped: the request path is the single writer — it records one
event after the upstream reports its token count — and everything on screen is a
reader.

```
proxy handler ──record()──▶ usage store        (write side, one writer)
                    └──────▶ Hub.Publish()     (notify)
                                 │
                            key actor          (owns that key's read model)
                                 │
                            subscribers        (one SSE stream per viewer)
```

`internal/live` runs **one goroutine per watched key**. That actor owns the key's
report and is the only thing that mutates it, so the read model needs no lock:
credits from the write side and periodic resyncs from the store are applied by
that single goroutine, in order. An actor exists only while somebody is watching
— the first subscriber starts it, the last to leave retires it — so nothing
accumulates for keys nobody has open, and a closed tab reaps its goroutine.

The dashboard is a **multi-page app**: `/admin/keys/{id}` is a real URL, so keys
are bookmarkable, shareable, and reachable with the back button. The page opens
exactly one stream with `data-init="@get('/admin/keys/{id}/live')"`, and the
server decides what to push and how often:

```go
for {
    select {
    case <-r.Context().Done(): return          // tab closed → unsubscribe → actor retires
    case <-changes:           push(queue)      // the queue moved: admitted, queued, released
    case <-cooldown.C:        push(queue)      // …coalesced to at most one frame per 100ms
    case <-heartbeat.C:       push(queue)      // 1s, so "oldest 3.2s" keeps counting
    case snap := <-snapshots: push(detail, row, total)   // the instant usage is recorded
    }
}
```

Nothing here is sampled. Both halves are event-driven — the queue signals every
admission, enqueue, grant, release and cancel; the key's actor signals every
recorded call — and the only timers are a 100 ms floor (so a burst of hundreds of
queue changes a second becomes one legible frame rather than hundreds) and a 1 s
heartbeat (so elapsed figures keep ticking while nothing changes).

Watchers can never slow the request path down: every signal is a non-blocking
send to a one-deep channel, so a watcher that is mid-render just coalesces what
it missed into its next wake-up.

Measured against a real Ollama in a browser: a recorded call reaches the screen
**~2 ms after the write**, and a request that queues shows up at **"6 queued ·
oldest 100ms"**. The previous polling dashboard showed usage up to 30 s late and
pool pressure up to 5 s late, and paid a query per viewer per tick to be that
stale.

One caveat: the sidebar figures for *other* keys are a page-load snapshot. Only
the key you are watching streams, which is the point of an actor per key.

## Pages

| Path | Who | What |
|------|-----|------|
| `/` | public | Landing page (Lithuanian): how to call the API, copy-paste curl for both endpoints, the real configured limits, the price and the status-code contract |
| `/kada-reikia-embeddingu-api` | public | Long-form article: when this service is worth it, when it is not, GDPR, cost, Lithuanian support. See `docs/seo.md` |
| `/robots.txt`, `/sitemap.xml`, `/llms.txt` | public | Crawler and assistant discovery |
| `/admin` | operator | Usage dashboard, behind Basic auth |
| `/healthz` | public | Liveness probe |

The landing page is built from the gateway's own config, so the limits it
advertises cannot drift from the ones actually enforced. It touches neither the
key store nor the usage store, so it cannot leak who holds a key or what they
spend.

### The published plan

One plan, priced from `PLAN_PRICE_EUR` / `PLAN_VAT_PERCENT` and described by the
same `DEFAULT_*` limits a new key is issued with — the page cannot sell terms the
gateway does not hand out:

| | |
|---|---|
| Price | **50 € + PVM / month** (60,50 € incl. 21% VAT) |
| Tokens | unlimited — no per-token charge |
| Rate | 100 requests/min per key |
| Batch | 25 inputs per request |

The same figures go out as a schema.org `Offer` on `/`, in the article's cost
section, and under `## Kiek kainuoja` in `llms.txt`, so an assistant asked what
this costs reads the price rather than inferring it. Set `PLAN_VAT_PERCENT=0`
and every surface drops the VAT wording instead of printing a meaningless `0%`.

## Dashboard access

The dashboard is operator-only — it lists every key, its limits and its spend —
so it lives under `/admin`, sits behind HTTP Basic auth, and **fails closed**:
with no `DASHBOARD_PASSWORD` set it is not served at all (`404`), and the
embeddings API carries on regardless. The whole surface is guarded (`/admin`,
`/admin/keys/{id}`, `/admin/queue` and its assets); only `/healthz` and the
landing page stay public.

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
cmd/ragctl         operator CLI: create/list/update/revoke keys
internal/config    .env + env configuration
internal/domain/   apikey + usage (pure business rules)
internal/ratelimit per-token fixed-window limiter
internal/budget    prepaid-allowance checker
internal/queue     priority admission queue in front of the pool
internal/live      CQRS read side: one goroutine per watched key
internal/proxy     /v1/embeddings enforcement pipeline + forwarder
internal/httpapi   chi router + Bearer-auth middleware
internal/web       datastar/templ dashboard (/admin) + public LT pages (/)
docs/seo.md        search-intent strategy behind the public pages
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
| `CONTACT_EMAIL` | `info@ituoga.lt` | Address published on the public pages for key requests |
| `CONTACT_PHONE` | `+37063594444` | Phone published alongside it (becomes a `tel:` link) |
| `COMPANY_URL` | `https://letas.lt` | Operator's site; linked publicly and named as the article's publisher |
| `DEFAULT_BATCH_MAX` | `25` | Default inputs/request per key |
| `DEFAULT_RATE_PER_MIN` | `100` | Default requests/min per key |
| `DEFAULT_TOKEN_BUDGET` | `-1` | Default token budget (`-1` = unlimited) |
| `DEFAULT_BUDGET_PERIOD` | `lifetime` | Default period (`monthly`/`lifetime`) |
| `DEFAULT_PRIORITY` | `0` | Default queue rank for a new key (`0`–`9`) |
| `PLAN_PRICE_EUR` | `50` | Published monthly price, excluding VAT |
| `PLAN_VAT_PERCENT` | `21` | VAT rate used to derive the published inclusive figure (`0` hides VAT entirely) |
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

# …or Ollama's native batch endpoint, same key, same limits:
curl http://localhost:8080/api/embed \
  -H "Authorization: Bearer sk-rag-…" \
  -H "Content-Type: application/json" \
  -d '{"model":"bge-m3","input":["hello","world"]}'

# 5. Open the usage dashboard (asks for DASHBOARD_USER / DASHBOARD_PASSWORD):
open http://localhost:8080/admin

# …and http://localhost:8080/ is the public page telling clients how to call it.
```

`ragctl list` shows every key with its limits, queue rank and month/lifetime
token usage; `ragctl revoke --id <n>` disables one.

Limits are editable in place, so raising a cap never means reissuing a key and
redeploying it. Only the flags you pass change; the rest are left alone:

```bash
ragctl update --id 3 --rate 1200 --batch 50
ragctl update --id 3 --budget 100000000 --period monthly
ragctl update --id 3 --priority 9
```

The command prints a before/after diff of exactly what moved, validates against
the same rules `create` uses, and refuses an unknown id rather than silently
changing nothing.

## Development

```bash
go test ./...            # unit + integration tests
go vet ./...
templ generate           # after editing internal/web/*.templ (never edit *_templ.go)
```

Migrations are goose SQL files under `migrations/`, embedded into the binary and
applied automatically at startup.
