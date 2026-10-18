package atlas

import (
	"context"
	"errors"
	"fmt"
)

// testLockID serialises every test, in any package or process, that resets
// the shared test database. go test runs packages in parallel processes.
const testLockID int64 = 0x4954454d42415445

// ResetForTests takes a cross-process lock on the test database, drops the
// atlas schema and returns the function that releases the lock. Hold it
// until the test ends, usually with t.Cleanup, so tests in other packages
// wait for the database instead of colliding with it. It exists for tests
// that need a disposable database and must never be reachable from a command.
func (s *Store) ResetForTests(ctx context.Context) (func(), error) {
	if s == nil || s.pool == nil {
		return nil, errNoDatabase
	}
	var name string
	if err := s.pool.QueryRow(ctx, "SELECT current_database()").Scan(&name); err != nil {
		return nil, err
	}
	if name == "" {
		return nil, errors.New("atlas: cannot determine the database to reset")
	}
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("atlas: cannot acquire a connection for the test lock: %w", err)
	}
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", testLockID); err != nil {
		conn.Release()
		return nil, fmt.Errorf("atlas: cannot take the test lock: %w", err)
	}
	release := func() {
		_, _ = conn.Exec(context.Background(), "SELECT pg_advisory_unlock($1)", testLockID)
		conn.Release()
	}
	if _, err := conn.Exec(ctx, "DROP SCHEMA IF EXISTS atlas CASCADE"); err != nil {
		release()
		return nil, fmt.Errorf("atlas: cannot reset the test database: %w", err)
	}
	return release, nil
}

// DropForTests drops the atlas schema again while the test lock from
// ResetForTests is still held, so one test can start several cases clean.
func (s *Store) DropForTests(ctx context.Context) error {
	if s == nil || s.pool == nil {
		return errNoDatabase
	}
	_, err := s.pool.Exec(ctx, "DROP SCHEMA IF EXISTS atlas CASCADE")
	return err
}
