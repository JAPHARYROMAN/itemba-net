package main

import (
	"fmt"
	"strings"
)

// Role selects which subsystems one process runs. The same binary serves the
// console, crawls, or verifies, so one deployment can span one machine or
// three without a second codebase.
type Role string

const (
	// RoleConsole serves the operator interface and the subsystems behind it.
	RoleConsole Role = "console"
	// RoleCrawler runs continuous discovery. It arrives with Phase 2.
	RoleCrawler Role = "crawler"
	// RoleProbe runs unattended verification. It arrives with Phase 4.
	RoleProbe Role = "probe"
	// RoleAll runs every available role in one process.
	RoleAll Role = "all"
)

// ParseRole accepts a role name, ignoring case and surrounding space.
func ParseRole(value string) (Role, error) {
	switch role := Role(strings.ToLower(strings.TrimSpace(value))); role {
	case RoleConsole, RoleCrawler, RoleProbe, RoleAll:
		return role, nil
	case "":
		return "", fmt.Errorf("a role is required: console, crawler, probe or all")
	default:
		return "", fmt.Errorf("unknown role %q: choose console, crawler, probe or all", value)
	}
}

// PendingPhase reports the implementation-plan phase that delivers a role's
// workers, or zero when the role can run now. Naming a role before it exists
// keeps the flag stable across phases while refusing, explicitly, to pretend
// it runs. The crawler arrived with Phase 2; the probe arrives with Phase 4.
func (r Role) PendingPhase() int {
	if r == RoleProbe {
		return 4
	}
	return 0
}

// RunsCrawler reports whether the role runs the continuous crawl.
func (r Role) RunsCrawler() bool {
	return r == RoleCrawler || r == RoleAll
}

// ServesConsole reports whether the role runs the operator interface.
func (r Role) ServesConsole() bool {
	return r == RoleConsole || r == RoleAll
}
