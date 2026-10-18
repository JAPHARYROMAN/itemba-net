// Package atlas is the persistent index described in the white paper. In
// Phase 0 it provides the PostgreSQL connection pool, embedded migrations and
// a health check. Phase 1 adds the schema and repositories.
package atlas

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store is a connection to the Atlas database. A nil Store means no database
// is configured; its methods report that explicitly rather than failing.
type Store struct {
	pool *pgxpool.Pool
}

// Open connects to PostgreSQL at url and verifies the connection within ctx.
// It does not run migrations; call Migrate. Errors never include the URL,
// which may carry a password.
func Open(ctx context.Context, url string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, errors.New("atlas: the database URL is not a valid PostgreSQL connection string")
	}
	cfg.MaxConns = 8
	cfg.MinConns = 1
	cfg.ConnConfig.ConnectTimeout = 5 * time.Second
	cfg.HealthCheckPeriod = 30 * time.Second

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, errors.New("atlas: cannot create the connection pool")
	}
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("atlas: database unreachable: %w", err)
	}
	return &Store{pool: pool}, nil
}

// Close releases every connection. It is safe on a nil Store.
func (s *Store) Close() {
	if s != nil && s.pool != nil {
		s.pool.Close()
	}
}

// Health describes the database as observed by one check.
type Health struct {
	// Configured is false when no database URL was given.
	Configured bool `json:"configured"`
	// Connected is true when the check query succeeded.
	Connected bool `json:"connected"`
	// ServerVersion is PostgreSQL's reported version when connected.
	ServerVersion string `json:"serverVersion,omitempty"`
	// Migrations is the number of applied migrations.
	Migrations int `json:"migrations"`
	// Pending is the number of embedded migrations not yet applied.
	Pending int `json:"pending"`
	// Error describes a failed check. It never contains credentials.
	Error string `json:"error,omitempty"`
}

// Health checks the connection and the migration state within ctx. A nil
// Store reports an unconfigured database.
func (s *Store) Health(ctx context.Context) Health {
	if s == nil || s.pool == nil {
		return Health{}
	}
	health := Health{Configured: true}
	all, err := list(migrationFiles)
	if err != nil {
		health.Error = err.Error()
		return health
	}
	health.Pending = len(all)

	var version string
	if err := s.pool.QueryRow(ctx, "SELECT current_setting('server_version')").Scan(&version); err != nil {
		health.Error = "database unreachable: " + err.Error()
		return health
	}
	health.Connected = true
	health.ServerVersion = version

	applied, err := appliedVersions(ctx, s.pool)
	if err != nil {
		health.Error = "migration state unavailable: " + err.Error()
		return health
	}
	health.Migrations = len(applied)
	health.Pending = 0
	for _, m := range all {
		if !applied[m.Version] {
			health.Pending++
		}
	}
	return health
}

// querier is the subset of pgxpool.Pool and pgxpool.Conn the package reads with.
type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// appliedVersions returns the versions recorded in the migrations table, or an
// empty set when the table does not exist yet.
func appliedVersions(ctx context.Context, q querier) (map[int]bool, error) {
	var table *string
	if err := q.QueryRow(ctx, "SELECT to_regclass('atlas.schema_migrations')::text").Scan(&table); err != nil {
		return nil, err
	}
	applied := make(map[int]bool)
	if table == nil {
		return applied, nil
	}
	rows, err := q.Query(ctx, "SELECT version FROM atlas.schema_migrations ORDER BY version")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var version int
		if err := rows.Scan(&version); err != nil {
			return nil, err
		}
		applied[version] = true
	}
	return applied, rows.Err()
}
