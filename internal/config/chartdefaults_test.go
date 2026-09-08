package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testdata/chart-defaults.yaml is the application.yaml the chart renders when no
// values are supplied — a file nobody writes by hand, and the one a bare
// `helm install` runs on. Regenerate it with:
//
//	helm template t ./charts/devops-tools -s templates/configmap.yaml
//
// and take the data.application.yaml out of the result.
func chartDefaults(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	src, err := os.ReadFile(filepath.Join("testdata", "chart-defaults.yaml"))
	if err != nil {
		t.Fatalf("read the rendered defaults: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "application.yaml"), src, 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// A bare install must not be signable into.
//
// It used to be: the chart shipped a local account whose password was "admin",
// so that `helm install` on its own produced a working login. That password was
// published in this repository, which makes it a known one rather than a weak
// one — and the installations it outlived were the ones whose owner never read
// as far as the warning.
//
// Refusing to start is the trade. The message has to say what to set, or this is
// just a worse first five minutes.
func TestChartDefaultsRefuseAnInstallWithNoWayIn(t *testing.T) {
	_, _, err := Load(NewFileLoader(chartDefaults(t)))
	if err == nil {
		t.Fatal("the chart's defaults start a portal nobody had to configure a password for")
	}
	for _, want := range []string{"auth.local.users", "passwordLogin"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not mention %q, so it does not say what to do: %v", want, err)
		}
	}
}

// And with that one password supplied — the whole of what an installation has
// to decide — the defaults have to hold together.
func TestChartDefaultsAreCoherentOnceAPasswordIsGiven(t *testing.T) {
	t.Setenv("DEVOPS_TOOLS_BOOTSTRAP_PASSWORD", "correct horse battery")

	cfg, rbac, err := Load(NewFileLoader(chartDefaults(t)))
	if err != nil {
		t.Fatalf("the chart's defaults do not load: %v", err)
	}

	if !cfg.Auth.Bootstrap.PasswordLogin.Enabled() {
		t.Error("the password was supplied and still nothing can sign in")
	}
	// The username has to be one of the accounts the roles are granted to, or
	// the sign-in works and reaches nothing.
	if !cfg.Auth.Bootstrap.Grants(cfg.Auth.Bootstrap.PasswordLogin.Username) {
		t.Errorf("passwordLogin signs in as %q, which bootstrapAdmins.users does not grant: %v",
			cfg.Auth.Bootstrap.PasswordLogin.Username, cfg.Auth.Bootstrap.Users)
	}
	for _, r := range cfg.Auth.Bootstrap.Roles {
		if _, ok := rbac.Roles[r]; !ok {
			t.Errorf("bootstrapAdmins grants %q, which the seeded model does not define", r)
		}
	}

	// The operations come from the build now. A seeded role naming something
	// this binary does not implement is refused by Load, so reaching here means
	// they line up — but the vocabulary must not be empty, which would mean the
	// derivation silently produced nothing.
	if len(rbac.Operations) == 0 {
		t.Error("no operations were derived from the build")
	}
}

// The published hash must not come back into the chart's defaults by way of an
// example somebody uncommented.
func TestChartDefaultsShipNoPassword(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("testdata", "chart-defaults.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(src), shippedHash) {
		t.Error("the chart's defaults carry the published password hash again")
	}
	if strings.Contains(string(src), "$2a$") || strings.Contains(string(src), "$2b$") {
		t.Error("the chart's defaults carry a bcrypt hash; a password shipped with the chart is a published one")
	}
}
