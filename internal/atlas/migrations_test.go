package atlas

import (
	"strings"
	"testing"
	"testing/fstest"
)

func TestEmbeddedMigrationsAreValid(t *testing.T) {
	migrations, err := list(migrationFiles)
	if err != nil {
		t.Fatal(err)
	}
	if len(migrations) == 0 || migrations[0].ID() != "0001_baseline" {
		t.Fatalf("unexpected embedded migrations: %+v", migrations)
	}
	for i, m := range migrations {
		if m.Version != i+1 || strings.TrimSpace(m.SQL) == "" {
			t.Fatalf("migration %d is malformed: %+v", i, m)
		}
	}
}

func TestListRefusesABrokenSetAsAWhole(t *testing.T) {
	cases := map[string]fstest.MapFS{
		"gap": {
			"migrations/0001_a.sql": {Data: []byte("SELECT 1;")},
			"migrations/0003_c.sql": {Data: []byte("SELECT 3;")},
		},
		"does not start at one": {
			"migrations/0002_b.sql": {Data: []byte("SELECT 2;")},
		},
		"duplicate version": {
			"migrations/0001_a.sql": {Data: []byte("SELECT 1;")},
			"migrations/0001_b.sql": {Data: []byte("SELECT 1;")},
		},
		"empty file": {
			"migrations/0001_a.sql": {Data: []byte("  \n")},
		},
		"malformed name": {
			"migrations/0001_a.sql": {Data: []byte("SELECT 1;")},
			"migrations/second.sql": {Data: []byte("SELECT 2;")},
		},
		"uppercase name": {
			"migrations/0001_Baseline.sql": {Data: []byte("SELECT 1;")},
		},
	}
	for name, fsys := range cases {
		t.Run(name, func(t *testing.T) {
			if migrations, err := list(fsys); err == nil {
				t.Fatalf("broken set was accepted: %+v", migrations)
			}
		})
	}
}

func TestListOrdersByVersionRegardlessOfDirectoryOrder(t *testing.T) {
	fsys := fstest.MapFS{
		"migrations/0003_c.sql": {Data: []byte("SELECT 3;")},
		"migrations/0001_a.sql": {Data: []byte("SELECT 1;")},
		"migrations/0002_b.sql": {Data: []byte("SELECT 2;")},
		"migrations/notes.txt":  {Data: []byte("ignored? no: refused, see below")},
	}
	if _, err := list(fsys); err == nil {
		t.Fatal("a stray non-migration file must be refused so a typo cannot hide a migration")
	}
	delete(fsys, "migrations/notes.txt")
	migrations, err := list(fsys)
	if err != nil {
		t.Fatal(err)
	}
	got := []string{migrations[0].ID(), migrations[1].ID(), migrations[2].ID()}
	if strings.Join(got, ",") != "0001_a,0002_b,0003_c" {
		t.Fatalf("wrong order: %v", got)
	}
}
