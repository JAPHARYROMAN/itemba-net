package atlas

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func TestOpenErrorsNeverEchoTheURL(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	const secret = "do-not-print-this-password"

	for name, url := range map[string]string{
		"unparseable": "postgres://itemba:" + secret + "@%zz/itemba",
		"unreachable": "postgres://itemba:" + secret + "@127.0.0.1:1/itemba?connect_timeout=2",
	} {
		t.Run(name, func(t *testing.T) {
			store, err := Open(ctx, url)
			if err == nil {
				store.Close()
				t.Fatal("expected an error")
			}
			if strings.Contains(err.Error(), secret) {
				t.Fatalf("error exposed the password: %v", err)
			}
		})
	}
}

func TestNilStoreReportsUnconfiguredAndRefusesToMigrate(t *testing.T) {
	var store *Store
	if health := store.Health(context.Background()); health.Configured || health.Connected {
		t.Fatalf("nil store must report unconfigured: %+v", health)
	}
	if _, err := store.Migrate(context.Background()); err == nil {
		t.Fatal("nil store must refuse to migrate")
	}
	store.Close()
}

// TestMigrateIsIdempotentAgainstPostgreSQL needs a disposable database named
// by ITEMBA_TEST_DATABASE_URL. It drops and recreates the atlas schema there.
// CI provides one; locally, docker compose up itemba-atlas and set
// ITEMBA_TEST_DATABASE_URL=postgres://itemba:itemba-dev@127.0.0.1:5432/itemba
func TestMigrateIsIdempotentAgainstPostgreSQL(t *testing.T) {
	url := os.Getenv("ITEMBA_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set ITEMBA_TEST_DATABASE_URL to a disposable PostgreSQL database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	store, err := Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	release, err := store.ResetForTests(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	all, err := list(migrationFiles)
	if err != nil {
		t.Fatal(err)
	}

	before := store.Health(ctx)
	if !before.Configured || !before.Connected || before.Migrations != 0 || before.Pending != len(all) || before.Error != "" {
		t.Fatalf("health before migrating: %+v", before)
	}

	applied, err := store.Migrate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(applied) != len(all) || applied[0] != "0001_baseline" {
		t.Fatalf("first run applied %v, want all %d migrations", applied, len(all))
	}

	again, err := store.Migrate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 0 {
		t.Fatalf("second run must apply nothing, applied %v", again)
	}

	after := store.Health(ctx)
	if !after.Connected || after.Migrations != len(all) || after.Pending != 0 || after.Error != "" || after.ServerVersion == "" {
		t.Fatalf("health after migrating: %+v", after)
	}

	var extension string
	if err := store.pool.QueryRow(ctx, "SELECT extname FROM pg_extension WHERE extname = 'vector'").Scan(&extension); err != nil {
		t.Fatalf("the vector extension must be installed by the baseline: %v", err)
	}
}
