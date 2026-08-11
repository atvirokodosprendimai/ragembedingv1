// Package migrations embeds the goose SQL migration files so the gateway ships
// as a single binary that can migrate its own database at startup — no external
// migration files to deploy alongside it.
package migrations

import "embed"

// FS holds every .sql migration in this directory. It is passed to goose as the
// base filesystem; the migrations sit at the FS root, so goose is pointed at ".".
//
//go:embed *.sql
var FS embed.FS
