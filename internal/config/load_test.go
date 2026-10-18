package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func clearTypedConfigEnv(t *testing.T) {
	t.Helper()
	for _, name := range allowedKeys {
		// Setenv registers restoration of the original environment before making
		// the variable absent for this test. These tests must not run in parallel.
		t.Setenv(name, "test-configuration-sentinel")
		if err := os.Unsetenv(name); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLoadDefaultsWithoutAFileOrEnvironment(t *testing.T) {
	clearTypedConfigEnv(t)
	cfg, err := Load(filepath.Join(t.TempDir(), ".env.local"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Port != "8080" || cfg.DatabaseURL != "" {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
}

func TestLoadReadsTheFileAndLetsTheEnvironmentWin(t *testing.T) {
	clearTypedConfigEnv(t)
	path := filepath.Join(t.TempDir(), ".env.local")
	if err := os.WriteFile(path, []byte("PORT=8089\nITEMBA_DATABASE_URL=postgres://itemba:file-secret@127.0.0.1:5432/itemba\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Port != "8089" || cfg.DatabaseURL != "postgres://itemba:file-secret@127.0.0.1:5432/itemba" {
		t.Fatalf("file values were not loaded: %+v", cfg)
	}

	t.Setenv("ITEMBA_DATABASE_URL", "postgres://itemba:env-secret@db.internal:5432/itemba")
	t.Setenv("PORT", "")
	cfg, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DatabaseURL != "postgres://itemba:env-secret@db.internal:5432/itemba" {
		t.Fatalf("environment did not take precedence over the file: %+v", cfg)
	}
	if cfg.Port != "8080" {
		t.Fatalf("an explicitly blank PORT must fall back to the default, got %q", cfg.Port)
	}
}

func TestLoadRejectsAnUnsupportedKeyWithoutEchoingValues(t *testing.T) {
	clearTypedConfigEnv(t)
	const sentinel = "do-not-print-this-test-value"
	path := filepath.Join(t.TempDir(), ".env.local")
	if err := os.WriteFile(path, []byte("ITEMBA_DATABASE_URL="+sentinel+"\nITEMBA_SECRET="+sentinel+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "line 2 ITEMBA_SECRET: unsupported configuration variable") {
		t.Fatalf("unsupported key was not rejected safely: %v", err)
	}
	if strings.Contains(err.Error(), sentinel) {
		t.Fatal("configuration diagnostic exposed a value")
	}
	if _, set := os.LookupEnv("ITEMBA_DATABASE_URL"); set {
		t.Fatal("a rejected file must not apply any of its values")
	}
}
