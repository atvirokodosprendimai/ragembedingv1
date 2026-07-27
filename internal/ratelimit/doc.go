// Package ratelimit enforces the per-token requests-per-minute ceiling.
//
// Limits differ per API key (each key carries its own RatePerMin), so this is a
// keyed fixed-window counter rather than a single global limiter. When a key
// exceeds its window the limiter reports the number of seconds until the window
// resets, which the HTTP layer turns into a 429 response with a Retry-After
// header so a well-behaved client knows exactly how long to back off.
//
// The window boundary is derived from an injectable clock so the rollover and
// Retry-After arithmetic can be tested deterministically.
package ratelimit
