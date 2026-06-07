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

// Open opens (and migrates) an ent client backed by a sqlite file at path.
// Use ":memory:" or a "file:...mode=memory" DSN for ephemeral databases.
func Open(ctx context.Context, path string) (*ent.Client, error) {
	dsn := fmt.Sprintf("file:%s?_fk=1&_busy_timeout=5000", path)
	client, err := ent.Open(dialect.SQLite, dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %q: %w", path, err)
	}
	if err := client.Schema.Create(ctx); err != nil {
		client.Close()
		return nil, fmt.Errorf("create schema: %w", err)
	}
	return client, nil
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
