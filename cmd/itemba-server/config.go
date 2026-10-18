package main

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	maxLocalConfigBytes = 16 * 1024
	maxLocalConfigLine  = 4 * 1024
)

// loadLocalConfig reads optional local process configuration. Explicit process
// environment variables take precedence, even when explicitly set to blank.
// Values are literal: no variable interpolation, shell commands, or escapes.
func loadLocalConfig(path string) error {
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("cannot open local configuration file")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("local configuration must be a regular file")
	}
	contents, err := io.ReadAll(io.LimitReader(file, maxLocalConfigBytes+1))
	if err != nil {
		return fmt.Errorf("cannot read local configuration file")
	}
	if len(contents) > maxLocalConfigBytes {
		return fmt.Errorf("local configuration exceeds %d bytes", maxLocalConfigBytes)
	}
	values, err := parseLocalConfig(contents)
	if err != nil {
		return err
	}
	// Parse everything before applying anything, so an invalid later line cannot
	// leave a partially applied configuration behind.
	for _, name := range []string{"PORT", "BRAVE_SEARCH_API_KEY", "TAVILY_API_KEY"} {
		value, present := values[name]
		if _, explicit := os.LookupEnv(name); !present || explicit {
			continue
		}
		if err := os.Setenv(name, value); err != nil {
			return fmt.Errorf("cannot apply %s from local configuration", name)
		}
	}
	return nil
}

func parseLocalConfig(contents []byte) (map[string]string, error) {
	if len(contents) > maxLocalConfigBytes {
		return nil, fmt.Errorf("local configuration exceeds %d bytes", maxLocalConfigBytes)
	}
	if !utf8.Valid(contents) {
		return nil, fmt.Errorf("local configuration must contain UTF-8 text")
	}
	text := strings.TrimPrefix(string(contents), "\ufeff")
	values := make(map[string]string)
	seen := make(map[string]bool)
	for i, raw := range strings.Split(text, "\n") {
		lineNumber := i + 1
		if len(raw) > maxLocalConfigLine {
			return nil, fmt.Errorf("line %d: configuration line exceeds %d bytes", lineNumber, maxLocalConfigLine)
		}
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, rawValue, assignment := strings.Cut(line, "=")
		name = strings.TrimSpace(name)
		if !assignment {
			// A missing equals sign must not echo a line that may contain a key.
			for _, allowed := range []string{"PORT", "BRAVE_SEARCH_API_KEY", "TAVILY_API_KEY"} {
				if line == allowed || strings.HasPrefix(line, allowed+" ") || strings.HasPrefix(line, allowed+"\t") || strings.HasPrefix(line, allowed+":") {
					return nil, fmt.Errorf("line %d %s: expected NAME=value", lineNumber, allowed)
				}
			}
			return nil, fmt.Errorf("line %d: expected NAME=value", lineNumber)
		}
		if !validConfigName(name) {
			return nil, fmt.Errorf("line %d: invalid configuration variable name", lineNumber)
		}
		if name != "PORT" && name != "BRAVE_SEARCH_API_KEY" && name != "TAVILY_API_KEY" {
			return nil, fmt.Errorf("line %d %s: unsupported configuration variable", lineNumber, name)
		}
		if seen[name] {
			return nil, fmt.Errorf("line %d %s: duplicate configuration variable", lineNumber, name)
		}
		seen[name] = true
		value, err := parseLocalConfigValue(rawValue)
		if err != nil {
			return nil, fmt.Errorf("line %d %s: %s", lineNumber, name, err)
		}
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if name == "PORT" {
			port, err := strconv.Atoi(value)
			if err != nil || port < 1 || port > 65535 || strings.Trim(value, "0123456789") != "" {
				return nil, fmt.Errorf("line %d PORT: expected a port number from 1 to 65535", lineNumber)
			}
		} else {
			for _, r := range value {
				if r < 33 || r > 126 {
					return nil, fmt.Errorf("line %d %s: expected a single token without whitespace or control characters", lineNumber, name)
				}
			}
		}
		values[name] = value
	}
	return values, nil
}

func validConfigName(name string) bool {
	if name == "" || len(name) > 64 {
		return false
	}
	for i, c := range name {
		if c != '_' && (c < 'A' || c > 'Z') && (c < 'a' || c > 'z') && (i == 0 || c < '0' || c > '9') {
			return false
		}
	}
	return true
}

func parseLocalConfigValue(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	if raw[0] == '\'' || raw[0] == '"' {
		quote := raw[0]
		for i := 1; i < len(raw); i++ {
			if raw[i] != quote {
				continue
			}
			tail := strings.TrimSpace(raw[i+1:])
			if tail != "" && !strings.HasPrefix(tail, "#") {
				return "", fmt.Errorf("unexpected text after quoted value")
			}
			return raw[1:i], nil
		}
		return "", fmt.Errorf("unterminated quoted value")
	}
	for i, c := range raw {
		if c == '#' && (i == 0 || raw[i-1] == ' ' || raw[i-1] == '\t') {
			return strings.TrimSpace(raw[:i]), nil
		}
	}
	return raw, nil
}
