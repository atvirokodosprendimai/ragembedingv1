// Package usage is the token-accounting domain: it records how many bge-m3 input
// tokens each API key has pushed through the proxy and rolls those records up
// into the time buckets the dashboard reports on.
//
// Token counts are authoritative values taken from Ollama's OpenAI-compatible
// response (usage.prompt_tokens) rather than estimated locally, so an Event is
// only written after a successful upstream call. The bucketing the product asks
// for — today, this week, this month, past month, and everything before — is
// defined here as pure time math so it can be tested without a database.
package usage
