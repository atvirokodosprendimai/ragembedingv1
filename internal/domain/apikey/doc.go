// Package apikey is the API-key domain: the entity a client authenticates with
// and the pure business rules that govern how much it may use the proxy.
//
// A key carries four independently configurable limits, defaulted from config
// and overridable per key by the ragctl CLI:
//
//   - BatchMax     — max number of inputs allowed in one /v1/embeddings call.
//   - RatePerMin   — max requests per minute (enforced by internal/ratelimit).
//   - TokenBudget  — cap on bge-m3 input tokens; -1 means unlimited (on-demand),
//     any positive value is a prepaid allowance.
//   - BudgetPeriod — whether TokenBudget is a monthly allowance (resets at the
//     calendar-month boundary) or a lifetime allowance (cumulative, never resets).
//
// Everything here is deliberately free of database, HTTP and clock concerns so
// the rules can be unit-tested in isolation; persistence lives behind Repository.
package apikey
