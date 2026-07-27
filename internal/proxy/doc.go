// Package proxy is the heart of the gateway: it authenticates, validates and
// rate-limits an incoming OpenAI-style /v1/embeddings request, forwards the
// accepted request to the Caddy upstream, and records the bge-m3 token usage
// reported by Ollama.
//
// The request path is deliberately ordered cheapest-check-first so abusive or
// malformed traffic is rejected before it costs an upstream call:
//
//	auth → decode → batch-size check → rate limit → budget check → forward → account
//
// Only after Caddy returns a successful response do we parse usage.prompt_tokens
// and persist a usage.Event; the upstream status and body are then streamed back
// to the client unchanged so the gateway is transparent to OpenAI SDK clients.
package proxy
