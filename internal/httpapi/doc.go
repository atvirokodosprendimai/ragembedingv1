// Package httpapi wires the domain and proxy packages onto a chi router: the
// Bearer-auth middleware, the /v1/embeddings proxy route, the health check, and
// the datastar dashboard routes.
//
// It owns HTTP concerns only — routing, middleware, status codes, headers — and
// delegates every decision to the domain and proxy packages so the transport
// layer stays thin and the business rules stay testable without a live server.
package httpapi
