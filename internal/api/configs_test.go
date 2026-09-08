package api

import "testing"

// The path arrives from the browser and is joined to the configured prefix
// before it reaches Consul. Anything that survives cleanConfigPath is
// concatenated verbatim, so escaping it would reach keys the grant never
// covered and the token was never meant to touch.
func TestConfigPathCannotEscapeThePrefix(t *testing.T) {
	rejected := []string{
		"",
		"/",
		"..",
		"../secrets/root-token",
		"abs/../../vault/token",
		"abs/../abs-plus/application.yml",
		"abs//application.yml",
		"./abs",
		"abs/./application.yml",
	}
	for _, p := range rejected {
		if got, err := cleanConfigPath(p); err == nil {
			t.Errorf("cleanConfigPath(%q) = %q, want an error", p, got)
		}
	}
}

// Traversal is rejected rather than normalised away. Rewriting "abs/../x" into
// "x" would pass a grant check on a path nobody typed; refusing it keeps the
// string the permission was checked against and the string Consul receives the
// same one.
func TestConfigPathIsNotSilentlyRewritten(t *testing.T) {
	if _, err := cleanConfigPath("abs/../abs"); err == nil {
		t.Error("traversal that resolves back inside the prefix was accepted; " +
			"it should be refused, not normalised")
	}
}

func TestConfigPathAcceptsOrdinaryKeys(t *testing.T) {
	cases := map[string]string{
		"abs":                     "abs",
		"abs/application.yml":     "abs/application.yml",
		"/abs/application.yml":    "abs/application.yml", // leading slash trimmed
		"abs/application.yml/":    "abs/application.yml", // trailing slash trimmed
		"abs/sub/dir/config.yaml": "abs/sub/dir/config.yaml",
	}
	for in, want := range cases {
		got, err := cleanConfigPath(in)
		if err != nil {
			t.Errorf("cleanConfigPath(%q) errored: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("cleanConfigPath(%q) = %q, want %q", in, got, want)
		}
	}
}
