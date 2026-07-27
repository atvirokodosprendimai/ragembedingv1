package database

import (
	"fmt"

	"github.com/glebarez/sqlite"
	"github.com/pressly/goose/v3"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/atvirokodosprendimai/ragembedingv1/migrations"
)

// Open opens the SQLite database at dbPath with the pure-Go glebarez driver
// (modernc under the hood), so the binary needs no cgo. GORM's own logger is
// silenced; request logging is the HTTP layer's job, not the ORM's.
func Open(dbPath string) (*gorm.DB, error) {
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		return nil, fmt.Errorf("database: open %q: %w", dbPath, err)
	}
	return db, nil
}

// Migrate applies every embedded goose migration against the same connection
// GORM uses, bringing an empty or out-of-date database up to the current schema.
// Running migrations here (rather than as a separate step) keeps schema and code
// deploying together as one binary.
func Migrate(db *gorm.DB) error {
	sqlDB, err := db.DB()
	if err != nil {
		return fmt.Errorf("database: sql handle: %w", err)
	}

	goose.SetBaseFS(migrations.FS)
	// Quiet goose's default stdout logging; migration success/failure is
	// reported through the returned error instead.
	goose.SetLogger(goose.NopLogger())
	if err := goose.SetDialect("sqlite3"); err != nil {
		return fmt.Errorf("database: set dialect: %w", err)
	}
	// "." because the embedded FS holds the .sql files at its root.
	if err := goose.Up(sqlDB, "."); err != nil {
		return fmt.Errorf("database: migrate up: %w", err)
	}
	return nil
}
