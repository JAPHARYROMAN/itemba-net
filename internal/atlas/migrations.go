package atlas

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// migrationFiles holds the schema as numbered SQL files. Each file is applied
// once, in order, inside its own transaction.
//
//go:embed migrations/*.sql
var migrationFiles embed.FS

// migrationLockID serialises concurrent migrators (a console, a crawler and a
// probe starting together) through a PostgreSQL advisory lock.
const migrationLockID int64 = 0x4954454d4241

const bootstrapSQL = `
CREATE SCHEMA IF NOT EXISTS atlas;
CREATE TABLE IF NOT EXISTS atlas.schema_migrations (
	version    integer PRIMARY KEY,
	name       text NOT NULL,
	applied_at timestamptz NOT NULL DEFAULT now()
);`

type migration struct {
	Version int
	Name    string
	SQL     string
}

// ID is the file stem, for example 0001_baseline.
func (m migration) ID() string {
	return fmt.Sprintf("%04d_%s", m.Version, m.Name)
}

var migrationFileName = regexp.MustCompile(`^(\d{4})_([a-z0-9_]+)\.sql$`)

// list reads the migrations directory of fsys and validates it: every file is
// NNNN_name.sql with non-empty SQL, and versions run from 1 without gaps or
// duplicates. A broken set is refused as a whole.
func list(fsys fs.FS) ([]migration, error) {
	entries, err := fs.ReadDir(fsys, "migrations")
	if err != nil {
		return nil, fmt.Errorf("atlas: cannot read migrations: %w", err)
	}
	var migrations []migration
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		match := migrationFileName.FindStringSubmatch(entry.Name())
		if match == nil {
			return nil, fmt.Errorf("atlas: migration %q must be named NNNN_name.sql", entry.Name())
		}
		version, _ := strconv.Atoi(match[1])
		body, err := fs.ReadFile(fsys, "migrations/"+entry.Name())
		if err != nil {
			return nil, fmt.Errorf("atlas: cannot read migration %q: %w", entry.Name(), err)
		}
		sql := strings.TrimSpace(string(body))
		if sql == "" {
			return nil, fmt.Errorf("atlas: migration %q is empty", entry.Name())
		}
		migrations = append(migrations, migration{Version: version, Name: match[2], SQL: sql})
	}
	sort.Slice(migrations, func(i, j int) bool { return migrations[i].Version < migrations[j].Version })
	for i, m := range migrations {
		if m.Version != i+1 {
			return nil, fmt.Errorf("atlas: migrations must run from 0001 without gaps or duplicates; found %s at position %d", m.ID(), i+1)
		}
	}
	return migrations, nil
}

// Migrate applies every pending migration in order and returns the IDs it
// applied. Each migration runs in its own transaction, and the whole run holds
// an advisory lock so concurrent processes wait rather than collide.
func (s *Store) Migrate(ctx context.Context) ([]string, error) {
	if s == nil || s.pool == nil {
		return nil, fmt.Errorf("atlas: no database configured")
	}
	all, err := list(migrationFiles)
	if err != nil {
		return nil, err
	}
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("atlas: cannot acquire a connection: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", migrationLockID); err != nil {
		return nil, fmt.Errorf("atlas: cannot take the migration lock: %w", err)
	}
	defer func() {
		_, _ = conn.Exec(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1)", migrationLockID)
	}()

	if _, err := conn.Exec(ctx, bootstrapSQL); err != nil {
		return nil, fmt.Errorf("atlas: cannot create the migrations table: %w", err)
	}
	applied, err := appliedVersions(ctx, conn)
	if err != nil {
		return nil, fmt.Errorf("atlas: cannot read applied migrations: %w", err)
	}

	var ids []string
	for _, m := range all {
		if applied[m.Version] {
			continue
		}
		tx, err := conn.Begin(ctx)
		if err != nil {
			return ids, fmt.Errorf("atlas: migration %s: %w", m.ID(), err)
		}
		if _, err := tx.Exec(ctx, m.SQL); err != nil {
			_ = tx.Rollback(ctx)
			return ids, fmt.Errorf("atlas: migration %s failed: %w", m.ID(), err)
		}
		if _, err := tx.Exec(ctx, "INSERT INTO atlas.schema_migrations (version, name) VALUES ($1, $2)", m.Version, m.Name); err != nil {
			_ = tx.Rollback(ctx)
			return ids, fmt.Errorf("atlas: migration %s could not be recorded: %w", m.ID(), err)
		}
		if err := tx.Commit(ctx); err != nil {
			return ids, fmt.Errorf("atlas: migration %s could not be committed: %w", m.ID(), err)
		}
		ids = append(ids, m.ID())
	}
	return ids, nil
}
