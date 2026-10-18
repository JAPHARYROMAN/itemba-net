package main

import "testing"

func TestParseRoleAcceptsKnownRolesIgnoringCaseAndSpace(t *testing.T) {
	cases := map[string]Role{
		"console":  RoleConsole,
		" Console": RoleConsole,
		"CRAWLER":  RoleCrawler,
		"probe ":   RoleProbe,
		"all":      RoleAll,
	}
	for input, want := range cases {
		got, err := ParseRole(input)
		if err != nil || got != want {
			t.Fatalf("ParseRole(%q) = %q, %v; want %q", input, got, err, want)
		}
	}
}

func TestParseRoleRejectsEmptyAndUnknownRoles(t *testing.T) {
	for _, input := range []string{"", "   ", "server", "console,crawler"} {
		if role, err := ParseRole(input); err == nil {
			t.Fatalf("ParseRole(%q) = %q, want an error", input, role)
		}
	}
}

func TestRolesRunnableInThisPhase(t *testing.T) {
	if RoleConsole.PendingPhase() != 0 || RoleAll.PendingPhase() != 0 || RoleCrawler.PendingPhase() != 0 {
		t.Fatal("console, crawler and all must be runnable now")
	}
	if RoleProbe.PendingPhase() != 4 {
		t.Fatal("probe must name the phase that delivers it")
	}
	if !RoleAll.RunsCrawler() || !RoleCrawler.RunsCrawler() || RoleConsole.RunsCrawler() {
		t.Fatal("crawler and all run the crawl; console does not")
	}
	if !RoleAll.ServesConsole() || !RoleConsole.ServesConsole() || RoleCrawler.ServesConsole() {
		t.Fatal("console and all serve the interface; crawler does not")
	}
}
