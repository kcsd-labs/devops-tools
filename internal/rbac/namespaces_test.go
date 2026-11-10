package rbac

import (
	"reflect"
	"testing"

	"devops-tools/internal/config"
)

func withNamespaceGrants(grants ...config.NamespaceGrant) *Authorizer {
	a := New(&config.RBACConfig{
		Roles: map[string]config.RoleSpec{"admin": {Namespaces: grants}},
	})
	a.ReserveFromWildcard([]string{"kube-*", "default"})
	return a
}

// "*" reaches every namespace but the cluster's own: in those it grants
// nothing at all, reading included.
func TestWildcardStopsAtReservedNamespaces(t *testing.T) {
	a := withNamespaceGrants(config.NamespaceGrant{Namespace: "*", Operations: []string{"*"}})
	roles := []string{"admin"}

	for _, ns := range []string{"payments-dev", "kubernetes-dashboard", "default-backend"} {
		if !a.Allowed(roles, ns, config.OpSecretDelete) {
			t.Errorf("%s: * should reach it", ns)
		}
	}
	for _, ns := range []string{"kube-system", "kube-public", "default"} {
		for _, op := range []string{config.OpLogs, config.OpSecretRead, config.OpSecretDelete, config.OpHelmUninstall} {
			if a.Allowed(roles, ns, op) {
				t.Errorf("%s %s: * should not reach it", ns, op)
			}
		}
	}
}

// Naming a reserved namespace is how a role is given it, and it works like any
// other grant — for the operations listed and no others.
func TestReservedNamespaceGrantedByName(t *testing.T) {
	a := withNamespaceGrants(
		config.NamespaceGrant{Namespace: "*", Operations: []string{"*"}},
		config.NamespaceGrant{Namespace: "kube-system", Operations: []string{config.OpLogs}},
	)
	roles := []string{"admin"}
	if !a.Allowed(roles, "kube-system", config.OpLogs) {
		t.Error("a grant naming kube-system should be honoured")
	}
	if a.Allowed(roles, "kube-system", config.OpSecretRead) {
		t.Error("naming kube-system for logs should not open its secrets through *")
	}
	if a.Allowed(roles, "kube-public", config.OpLogs) {
		t.Error("naming kube-system should not reach kube-public")
	}
	if got := a.NamedNamespaces(roles, ""); !reflect.DeepEqual(got, []string{"kube-system"}) {
		t.Errorf("NamedNamespaces = %v", got)
	}
	if got := a.NamedNamespaces(roles, config.OpServiceList); len(got) != 0 {
		t.Errorf("NamedNamespaces for service-list = %v, want none", got)
	}
}

// With nothing reserved — cluster.hiddenNamespaces: [] — "*" means everything.
func TestNothingReservedMeansEverything(t *testing.T) {
	a := New(&config.RBACConfig{Roles: map[string]config.RoleSpec{
		"admin": {Namespaces: []config.NamespaceGrant{{Namespace: "*", Operations: []string{"*"}}}},
	}})
	if !a.Allowed([]string{"admin"}, "kube-system", config.OpLogs) {
		t.Error("with nothing reserved, * should reach kube-system")
	}
}

// A shell is granted only where it is written out: "everything" in a namespace
// is every other operation, not this one.
func TestPodExecIsNotCoveredByEverything(t *testing.T) {
	a := withNamespaceGrants(config.NamespaceGrant{Namespace: "abs-dev", Operations: []string{"*"}})
	roles := []string{"admin"}
	if !a.Allowed(roles, "abs-dev", config.OpSecretDelete) {
		t.Fatal("* should still cover the ordinary operations")
	}
	if a.Allowed(roles, "abs-dev", config.OpPodExec) {
		t.Error("* should not grant pod-exec")
	}

	a = withNamespaceGrants(config.NamespaceGrant{Namespace: "abs-dev", Operations: []string{"*", config.OpPodExec}})
	if !a.Allowed(roles, "abs-dev", config.OpPodExec) {
		t.Error("pod-exec written out beside * should be granted")
	}
	if got := a.NamespacesForOp(roles, config.OpPodExec); len(got) != 1 || got[0] != "abs-dev" {
		t.Errorf("NamespacesForOp(pod-exec) = %v", got)
	}
}

// No grant opens a shell in the cluster's namespaces or the portal's — not "*",
// and not one naming them.
func TestPodExecNeverInClosedNamespaces(t *testing.T) {
	a := withNamespaceGrants(
		config.NamespaceGrant{Namespace: "*", Operations: []string{config.OpPodExec}},
		config.NamespaceGrant{Namespace: "kube-system", Operations: []string{config.OpPodExec, config.OpLogs}},
		config.NamespaceGrant{Namespace: "devops-tools", Operations: []string{config.OpPodExec}},
	)
	a.CloseToExec([]string{"kube-*", "default", "devops-tools"})
	roles := []string{"admin"}

	if !a.Allowed(roles, "abs-dev", config.OpPodExec) {
		t.Error("pod-exec on * should reach an ordinary namespace")
	}
	for _, ns := range []string{"kube-system", "default", "devops-tools"} {
		if a.Allowed(roles, ns, config.OpPodExec) {
			t.Errorf("%s: a shell should never be granted there", ns)
		}
	}
	if !a.Allowed(roles, "kube-system", config.OpLogs) {
		t.Error("closing kube-system to a shell should not close it to a grant naming it for logs")
	}
}
