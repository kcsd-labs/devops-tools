// Package rbac decides whether a user's roles allow an operation in a
// namespace. It is the inner gate: the service account defines what the tool
// could ever do, this decides what a given user may actually do.
package rbac

import (
	"sort"
	"strings"
	"sync/atomic"

	"devops-tools/internal/config"
)

// Authorizer answers access questions from the current model. The model is
// held in an atomic pointer so it can be swapped while requests are in flight,
// which is what makes hot reload safe.
type Authorizer struct {
	cfg atomic.Pointer[config.RBACConfig]
}

func New(cfg *config.RBACConfig) *Authorizer {
	a := &Authorizer{}
	a.cfg.Store(cfg)
	return a
}

// Replace swaps in a new model atomically; called by the configuration watcher.
func (a *Authorizer) Replace(cfg *config.RBACConfig) {
	a.cfg.Store(cfg)
}

// Allowed reports whether any of the user's roles permits op in namespace.
func (a *Authorizer) Allowed(roles []string, namespace, op string) bool {
	cfg := a.cfg.Load()
	for _, role := range roles {
		spec, ok := cfg.Roles[role]
		if !ok {
			continue
		}
		for _, grant := range spec.Namespaces {
			if grant.Namespace != "*" && grant.Namespace != namespace {
				continue
			}
			if hasOp(grant.Operations, op) {
				return true
			}
		}
	}
	return false
}

// AllowedGlobal reports whether any of the user's roles permits an operation
// that is not tied to a namespace. Only an explicit global grant counts — see
// RoleSpec.Global for why a namespace wildcard does not.
func (a *Authorizer) AllowedGlobal(roles []string, op string) bool {
	cfg := a.cfg.Load()
	for _, role := range roles {
		spec, ok := cfg.Roles[role]
		if !ok {
			continue
		}
		if hasOp(spec.Global, op) {
			return true
		}
	}
	return false
}

// AllowedConfig reports whether any role permits op on a configuration path.
func (a *Authorizer) AllowedConfig(roles []string, path, op string) bool {
	cfg := a.cfg.Load()
	for _, role := range roles {
		spec, ok := cfg.Roles[role]
		if !ok {
			continue
		}
		for _, g := range spec.Configs {
			if configPathCovers(g.Path, path) && hasOp(g.Operations, op) {
				return true
			}
		}
	}
	return false
}

// configPathCovers reports whether a grant on one path reaches another.
//
// The comparison is by path segment, not by string prefix, and that is the
// whole point of it: with folders named abs and abs-plus side by side, a prefix
// test would silently hand the holder of abs everything in abs-plus. Nothing in
// the interface would show it — the access would simply be wider than what was
// granted.
func configPathCovers(grant, path string) bool {
	if grant == "*" {
		return true
	}
	grant = strings.Trim(grant, "/")
	path = strings.Trim(path, "/")
	return grant == path || strings.HasPrefix(path, grant+"/")
}

// VisibleConfigPaths lists the configuration paths the user may see. A wildcard
// grant returns ["*"], as VisibleNamespaces does for namespaces.
func (a *Authorizer) VisibleConfigPaths(roles []string) []string {
	cfg := a.cfg.Load()
	set := map[string]bool{}
	for _, role := range roles {
		spec, ok := cfg.Roles[role]
		if !ok {
			continue
		}
		for _, g := range spec.Configs {
			if g.Path == "*" {
				return []string{"*"}
			}
			set[g.Path] = true
		}
	}
	return sortedKeys(set)
}

// VisibleNamespaces lists the namespaces the user may see. A wildcard grant
// returns ["*"], which the caller presents as free-form input rather than
// enumerating every namespace in the cluster.
func (a *Authorizer) VisibleNamespaces(roles []string) []string {
	cfg := a.cfg.Load()
	set := map[string]bool{}
	for _, role := range roles {
		spec, ok := cfg.Roles[role]
		if !ok {
			continue
		}
		for _, grant := range spec.Namespaces {
			if grant.Namespace == "*" {
				return []string{"*"}
			}
			set[grant.Namespace] = true
		}
	}
	return sortedKeys(set)
}

func hasOp(ops []string, op string) bool {
	for _, o := range ops {
		if o == "*" || o == op {
			return true
		}
	}
	return false
}

func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
