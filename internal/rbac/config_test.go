package rbac

import (
	"testing"

	"devops-tools/internal/config"
)

func withConfigGrants(grants ...config.ConfigGrant) *Authorizer {
	return New(&config.RBACConfig{
		Operations: []string{"config-read", "config-write"},
		Roles:      map[string]config.RoleSpec{"team": {Configs: grants}},
	})
}

// The reason this comparison is by segment and not by prefix. These two folders
// sit side by side in a real tree; a prefix test hands the holder of one
// everything in the other, and nothing in the interface would show it.
func TestGrantOnAbsDoesNotReachAbsPlus(t *testing.T) {
	a := withConfigGrants(config.ConfigGrant{Path: "abs", Operations: []string{"config-write"}})

	if !a.AllowedConfig([]string{"team"}, "abs", "config-write") {
		t.Fatal("the granted path itself was refused")
	}
	if !a.AllowedConfig([]string{"team"}, "abs/application.yml", "config-write") {
		t.Fatal("a file under the granted path was refused")
	}
	if a.AllowedConfig([]string{"team"}, "abs-plus", "config-write") {
		t.Fatal("a grant on abs reached abs-plus")
	}
	if a.AllowedConfig([]string{"team"}, "abs-plus/application.yml", "config-write") {
		t.Fatal("a grant on abs reached a file in abs-plus")
	}
}

func TestConfigOperationsAreSeparate(t *testing.T) {
	a := withConfigGrants(config.ConfigGrant{Path: "billing", Operations: []string{"config-read"}})

	if !a.AllowedConfig([]string{"team"}, "billing/application.yml", "config-read") {
		t.Fatal("reading was refused")
	}
	// Seeing a configuration and changing it are different grants.
	if a.AllowedConfig([]string{"team"}, "billing/application.yml", "config-write") {
		t.Fatal("a read grant allowed writing")
	}
}

func TestConfigWildcards(t *testing.T) {
	all := withConfigGrants(config.ConfigGrant{Path: "*", Operations: []string{"config-read"}})
	if !all.AllowedConfig([]string{"team"}, "anything/at/all.yml", "config-read") {
		t.Fatal("a wildcard path granted nothing")
	}
	everyOp := withConfigGrants(config.ConfigGrant{Path: "abs", Operations: []string{"*"}})
	if !everyOp.AllowedConfig([]string{"team"}, "abs/x.yml", "config-write") {
		t.Fatal("a wildcard operation granted nothing")
	}
}

// A namespace grant says nothing about configuration, and the reverse.
func TestNamespaceAndConfigDoNotImplyEachOther(t *testing.T) {
	a := New(&config.RBACConfig{
		Operations: []string{"logs", "config-write"},
		Roles: map[string]config.RoleSpec{
			"team": {
				Namespaces: []config.NamespaceGrant{{Namespace: "*", Operations: []string{"*"}}},
			},
		},
	})
	if a.AllowedConfig([]string{"team"}, "abs", "config-write") {
		t.Fatal("a wildcard namespace grant reached configuration")
	}

	b := withConfigGrants(config.ConfigGrant{Path: "*", Operations: []string{"*"}})
	if b.Allowed([]string{"team"}, "payments-dev", "logs") {
		t.Fatal("a wildcard configuration grant reached a namespace")
	}
}

func TestUnknownRoleAndPathGrantNothing(t *testing.T) {
	a := withConfigGrants(config.ConfigGrant{Path: "abs", Operations: []string{"config-read"}})
	if a.AllowedConfig([]string{"nobody"}, "abs", "config-read") {
		t.Fatal("an unknown role was allowed")
	}
	if a.AllowedConfig([]string{"team"}, "billing", "config-read") {
		t.Fatal("an ungranted path was allowed")
	}
	if a.AllowedConfig(nil, "abs", "config-read") {
		t.Fatal("no roles at all were allowed")
	}
}

// Slashes around a path must not change what it covers.
func TestTrailingSlashesAreIgnored(t *testing.T) {
	a := withConfigGrants(config.ConfigGrant{Path: "abs/", Operations: []string{"config-read"}})
	if !a.AllowedConfig([]string{"team"}, "/abs/application.yml", "config-read") {
		t.Fatal("a stray slash changed the outcome")
	}
}

func TestVisibleConfigPaths(t *testing.T) {
	a := withConfigGrants(
		config.ConfigGrant{Path: "billing", Operations: []string{"config-read"}},
		config.ConfigGrant{Path: "abs", Operations: []string{"config-read"}},
	)
	got := a.VisibleConfigPaths([]string{"team"})
	if len(got) != 2 || got[0] != "abs" || got[1] != "billing" {
		t.Fatalf("paths = %v, want them sorted", got)
	}
	all := withConfigGrants(config.ConfigGrant{Path: "*", Operations: []string{"*"}})
	if got := all.VisibleConfigPaths([]string{"team"}); len(got) != 1 || got[0] != "*" {
		t.Fatalf("wildcard should collapse to [*], got %v", got)
	}
}
