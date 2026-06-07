package db

import (
	"context"
	"fmt"

	"entgo.io/ent/dialect"
	"github.com/gartnera/gofin/ent"
	// CGO sqlite driver, registered under the name "sqlite3" which matches
	// ent's dialect.SQLite.
	_ "github.com/mattn/go-sqlite3"
)

// Open opens an ent client backed by a sqlite file at path. It does not run
// migrations: the schema is owned by Migrate (invoked by `serve` and the
// `migrate` command) so that DDL only ever runs from a single process. Use
// ":memory:" or a "file:...mode=memory" DSN for ephemeral databases.
func Open(ctx context.Context, path string) (*ent.Client, error) {
	// WAL + synchronous=NORMAL keep the per-row inserts a large scan performs
	// from each paying a full fsync, which is the dominant cost when indexing
	// tens of thousands of files; WAL also lets the watcher/refresh writes
	// proceed without blocking concurrent reads from HTTP handlers.
	dsn := fmt.Sprintf("file:%s?_fk=1&_busy_timeout=5000&_journal_mode=WAL&_synchronous=NORMAL", path)
	client, err := ent.Open(dialect.SQLite, dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %q: %w", path, err)
	}
	return client, nil
}

// Migrate creates or updates the database schema to match the ent models. It is
// the single owner of DDL: `serve` runs it on startup and `migrate` runs it on
// demand, so short-lived commands like `user add` can Open without taking a
// schema lock against a live server.
func Migrate(ctx context.Context, client *ent.Client) error {
	if err := client.Schema.Create(ctx); err != nil {
		return fmt.Errorf("create schema: %w", err)
	}
	return nil
}

// OpenMemory opens a fresh in-memory database, primarily for tests. Each call
// returns an isolated database via a unique shared-cache name.
func OpenMemory(ctx context.Context, name string) (*ent.Client, error) {
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared&_fk=1", name)
	client, err := ent.Open(dialect.SQLite, dsn)
	if err != nil {
		return nil, fmt.Errorf("open memory db: %w", err)
	}
	if err := client.Schema.Create(ctx); err != nil {
		client.Close()
		return nil, fmt.Errorf("create schema: %w", err)
	}
	return client, nil
}
