package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func clearTestConfigEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{"PORT", "BRAVE_SEARCH_API_KEY", "TAVILY_API_KEY"} {
		// Setenv registers restoration of the original environment before making
		// the variable absent for this test. These tests must not run in parallel.
		t.Setenv(name, "test-configuration-sentinel")
		if err := os.Unsetenv(name); err != nil {
			t.Fatal(err)
		}
	}
}

func writeTestConfig(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), ".env.local")
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLocalConfigLoadsOnlyAllowedProcessSettings(t *testing.T) {
	clearTestConfigEnv(t)
	path := writeTestConfig(t, "# Local development\r\nPORT = '8089' # Port comment\r\nBRAVE_SEARCH_API_KEY=\"test-brave-token#literal\" # Key comment\r\nTAVILY_API_KEY='test-tavily-token' # Alternate provider\r\n")
	if err := loadLocalConfig(path); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("PORT") != "8089" || os.Getenv("BRAVE_SEARCH_API_KEY") != "test-brave-token#literal" || os.Getenv("TAVILY_API_KEY") != "test-tavily-token" {
		t.Fatal("allowed local configuration was not applied correctly")
	}
}

func TestLocalConfigPreservesExplicitEnvironmentIncludingBlank(t *testing.T) {
	clearTestConfigEnv(t)
	t.Setenv("PORT", "8099")
	t.Setenv("BRAVE_SEARCH_API_KEY", "test-process-token")
	t.Setenv("TAVILY_API_KEY", "test-process-tavily-token")
	path := writeTestConfig(t, "PORT=8089\nBRAVE_SEARCH_API_KEY=test-file-token\nTAVILY_API_KEY=test-file-tavily-token\n")
	if err := loadLocalConfig(path); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("PORT") != "8099" || os.Getenv("BRAVE_SEARCH_API_KEY") != "test-process-token" || os.Getenv("TAVILY_API_KEY") != "test-process-tavily-token" {
		t.Fatal("file configuration replaced explicit environment variables")
	}
	t.Setenv("BRAVE_SEARCH_API_KEY", "")
	t.Setenv("TAVILY_API_KEY", "")
	if err := loadLocalConfig(path); err != nil {
		t.Fatal(err)
	}
	if value, explicit := os.LookupEnv("BRAVE_SEARCH_API_KEY"); !explicit || value != "" {
		t.Fatal("an explicitly blank environment variable was replaced")
	}
	if value, explicit := os.LookupEnv("TAVILY_API_KEY"); !explicit || value != "" {
		t.Fatal("an explicitly blank Tavily environment variable was replaced")
	}
}

func TestMissingAndBlankLocalConfigDoNotConfigureBrave(t *testing.T) {
	clearTestConfigEnv(t)
	if err := loadLocalConfig(filepath.Join(t.TempDir(), ".env.local")); err != nil {
		t.Fatalf("optional missing file failed: %v", err)
	}
	path := writeTestConfig(t, "PORT=\nBRAVE_SEARCH_API_KEY=\" \" # No key configured\nTAVILY_API_KEY= # No alternate key configured\n")
	if err := loadLocalConfig(path); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"PORT", "BRAVE_SEARCH_API_KEY", "TAVILY_API_KEY"} {
		if _, present := os.LookupEnv(name); present {
			t.Errorf("blank local configuration created %s", name)
		}
	}
}

func TestLocalConfigParsesQuotesCommentsAndLiteralValues(t *testing.T) {
	for _, tc := range []struct {
		name, contents string
		want           map[string]string
	}{
		{"comments and whitespace", "  # comment\n\t\nPORT=8088 # note\nBRAVE_SEARCH_API_KEY=test-token # note", map[string]string{"PORT": "8088", "BRAVE_SEARCH_API_KEY": "test-token"}},
		{"hash is literal inside token", "BRAVE_SEARCH_API_KEY=test#token", map[string]string{"BRAVE_SEARCH_API_KEY": "test#token"}},
		{"single quotes", "BRAVE_SEARCH_API_KEY='test#token' # note", map[string]string{"BRAVE_SEARCH_API_KEY": "test#token"}},
		{"double quotes", "BRAVE_SEARCH_API_KEY=\"test#token\"#note", map[string]string{"BRAVE_SEARCH_API_KEY": "test#token"}},
		{"no variable expansion", "BRAVE_SEARCH_API_KEY='${TEST_UNUSED_TOKEN}'", map[string]string{"BRAVE_SEARCH_API_KEY": "${TEST_UNUSED_TOKEN}"}},
		{"no shell expansion", "BRAVE_SEARCH_API_KEY='$(test-placeholder)'", map[string]string{"BRAVE_SEARCH_API_KEY": "$(test-placeholder)"}},
		{"UTF8 BOM and CRLF", "\ufeffPORT=8088\r\nBRAVE_SEARCH_API_KEY=test-token\r\n", map[string]string{"PORT": "8088", "BRAVE_SEARCH_API_KEY": "test-token"}},
		{"Tavily quotes and comments", "TAVILY_API_KEY=\"test-tavily#token\" # note", map[string]string{"TAVILY_API_KEY": "test-tavily#token"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseLocalConfig([]byte(tc.contents))
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatal("configuration parsing did not preserve the expected literal values")
			}
		})
	}
}

func TestLocalConfigDiagnosticsNeverEchoValues(t *testing.T) {
	const sentinel = "do-not-print-this-test-value"
	for _, tc := range []struct {
		name, line, diagnostic string
	}{
		{"unterminated quote", "BRAVE_SEARCH_API_KEY='" + sentinel, "BRAVE_SEARCH_API_KEY: unterminated quoted value"},
		{"trailing text", "BRAVE_SEARCH_API_KEY=\"" + sentinel + "\" extra", "BRAVE_SEARCH_API_KEY: unexpected text after quoted value"},
		{"internal whitespace", "BRAVE_SEARCH_API_KEY='" + sentinel + " other'", "BRAVE_SEARCH_API_KEY: expected a single token"},
		{"control character", "BRAVE_SEARCH_API_KEY=" + sentinel + "\x00", "BRAVE_SEARCH_API_KEY: expected a single token"},
		{"missing equals", "BRAVE_SEARCH_API_KEY: " + sentinel, "BRAVE_SEARCH_API_KEY: expected NAME=value"},
		{"Tavily unterminated quote", "TAVILY_API_KEY='" + sentinel, "TAVILY_API_KEY: unterminated quoted value"},
		{"Tavily internal whitespace", "TAVILY_API_KEY='" + sentinel + " other'", "TAVILY_API_KEY: expected a single token"},
		{"Tavily control character", "TAVILY_API_KEY=" + sentinel + "\x00", "TAVILY_API_KEY: expected a single token"},
		{"Tavily missing equals", "TAVILY_API_KEY: " + sentinel, "TAVILY_API_KEY: expected NAME=value"},
		{"unsupported name", "UNRELATED_SETTING=" + sentinel, "UNRELATED_SETTING: unsupported configuration variable"},
		{"malformed name", "BAD-NAME=" + sentinel, "invalid configuration variable name"},
		{"invalid port", "PORT=" + sentinel, "PORT: expected a port number"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseLocalConfig([]byte("# comment\n" + tc.line))
			if err == nil || !strings.Contains(err.Error(), "line 2") || !strings.Contains(err.Error(), tc.diagnostic) {
				t.Fatalf("missing safe line/key diagnostic: %v", err)
			}
			if strings.Contains(err.Error(), sentinel) {
				t.Fatal("configuration diagnostic exposed a value")
			}
		})
	}
}

func TestMalformedLocalConfigDoesNotPartiallyApplySettings(t *testing.T) {
	clearTestConfigEnv(t)
	path := writeTestConfig(t, "PORT=8089\nBRAVE_SEARCH_API_KEY='test-unterminated\n")
	if err := loadLocalConfig(path); err == nil {
		t.Fatal("malformed configuration was accepted")
	}
	for _, name := range []string{"PORT", "BRAVE_SEARCH_API_KEY", "TAVILY_API_KEY"} {
		if _, present := os.LookupEnv(name); present {
			t.Errorf("malformed configuration partially applied %s", name)
		}
	}
}

func TestLocalConfigRejectsDuplicatesInvalidPortsAndOversizedFiles(t *testing.T) {
	for _, contents := range []string{
		"PORT=8080\nPORT=8081",
		"BRAVE_SEARCH_API_KEY=test-one\nBRAVE_SEARCH_API_KEY=test-two",
		"TAVILY_API_KEY=test-one\nTAVILY_API_KEY=test-two",
		"PORT=0",
		"PORT=65536",
		"PORT=+8080",
		"BRAVE_SEARCH_API_KEY=" + strings.Repeat("x", maxLocalConfigLine),
		strings.Repeat("x", maxLocalConfigBytes+1),
		string([]byte{0xff, 0xfe}),
	} {
		if _, err := parseLocalConfig([]byte(contents)); err == nil {
			t.Fatal("invalid or oversized configuration was accepted")
		}
	}
	clearTestConfigEnv(t)
	path := writeTestConfig(t, strings.Repeat("x", maxLocalConfigBytes+1))
	if err := loadLocalConfig(path); err == nil {
		t.Fatal("oversized file was accepted")
	}
	if err := loadLocalConfig(t.TempDir()); err == nil {
		t.Fatal("directory was accepted as local configuration")
	}
}
