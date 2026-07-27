// Package config loads gateway configuration from the process environment and
// an optional .env file, applying documented defaults.
//
// The gateway is deployed as a single binary in front of a Caddy load balancer,
// so every tunable (per-token defaults, the Caddy upstream URL, the embedding
// model, the SQLite path) has to be resolvable from the environment alone with
// sane fallbacks. This package is the one place those defaults live.
package config
