// Package web renders the terminal/data-ops usage dashboard with templ and drives
// it with datastar (server-rendered HTML fragments over SSE, no bespoke JS).
//
// The dashboard is read-only and operator-facing: it shows, per API key, the
// bge-m3 input tokens consumed across the reporting buckets (today, this week,
// this month, past month, and everything before) together with each key's
// configured limits. Rendering is a pure function of a view-model struct so the
// same fragment code serves both the initial page and datastar patch responses.
package web
