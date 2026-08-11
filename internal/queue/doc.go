// Package queue admits embedding requests to the shared Ollama pool in priority
// order.
//
// The pool behind Caddy has a finite number of concurrent slots. Without
// admission control every client competes for them equally, so a batch job
// hammering the gateway makes the interactive site wait behind it. This package
// puts a bounded, priority-aware gate in front of the upstream call:
//
//	acquire slot ──▶ forward to Caddy ──▶ release slot
//
// Scheduling has two rules, in this order:
//
//  1. Anti-starvation. A request queued longer than promoteAfter is admitted
//     ahead of everything else, oldest first. Free traffic therefore always
//     drains — it is never indefinitely postponed by priority work.
//  2. Strict priority. Otherwise the highest-priority waiter goes first, FIFO
//     within a priority class.
//
// Released slots are handed directly to the chosen waiter rather than returned
// to a free pool, so a newly arrived request can never barge past a queue.
//
// Waiting is unbounded by design: a queued request waits until a slot frees or
// its own request context is cancelled (client hang-up or gateway shutdown).
// Because every waiter is an in-flight HTTP request, the queue is implicitly
// bounded by the server's connection count, and a client that gives up removes
// itself from the queue.
package queue
