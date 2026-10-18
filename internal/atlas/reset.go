package atlas

import (
	"context"
	"errors"
)

// ResetForTests drops the atlas schema so a test can migrate it fresh. It
// exists for tests in other packages that need a disposable database and
// must never be reachable from a command.
func (s *Store) ResetForTests(ctx context.Context) error {
	if s == nil || s.pool == nil {
		return errNoDatabase
	}
	var name string
	if err := s.pool.QueryRow(ctx, "SELECT current_database()").Scan(&name); err != nil {
		return err
	}
	if name == "" {
		return errors.New("atlas: cannot determine the database to reset")
	}
	_, err := s.pool.Exec(ctx, "DROP SCHEMA IF EXISTS atlas CASCADE")
	return err
}
