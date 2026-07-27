// Package database opens the SQLite store with a pure-Go (cgo-free) driver and
// applies the goose SQL migrations before the application uses it.
//
// The no-cgo requirement is a hard constraint: we use github.com/glebarez/sqlite
// (which wraps modernc.org/sqlite) as the GORM driver so the gateway cross-
// compiles and ships as a static binary. Schema changes are goose .sql files
// embedded into the binary and run against the same underlying *sql.DB that GORM
// uses, keeping migrations and the ORM pointed at one connection.
package database
