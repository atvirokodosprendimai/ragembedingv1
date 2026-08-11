// Package proxy is the heart of the gateway: it authenticates, validates and
// rate-limits an incoming OpenAI-style /v1/embeddings request, forwards the
// accepted request to the Caddy upstream, and records the bge-m3 token usage
// reported by Ollama.
//
// The request path is deliberately ordered cheapest-check-first so abusive or
// malformed traffic is rejected before it costs an upstream call, and only a
// legitimate request ever competes for a slot in the Ollama pool:
//
//	auth → decode → batch-size check → rate limit → budget check → admit → forward → account
//
// "admit" is the priority queue (internal/queue): it bounds how many requests
// are in flight upstream and serves higher-priority keys first, so the
// operator's own site is not stuck behind a batch client's flood.
//
// Only after Caddy returns a successful response do we parse usage.prompt_tokens
// and persist a usage.Event; the upstream status and body are then streamed back
// to the client unchanged so the gateway is transparent to OpenAI SDK clients.
package proxy
