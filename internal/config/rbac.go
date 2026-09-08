package config

import "fmt"

// RBACConfig is the access model: which roles may perform which operations in
// which namespaces. Roles come from the authentication provider (local user
// roles, LDAP group mapping, or IdP roles).
type RBACConfig struct {
	// Operations is the vocabulary a role may draw on. It comes from the code —
	// see Operations — and is not something a deployment writes.
	Operations []string `yaml:"-" json:"operations"`
	// Declared is whatever a configuration still lists under `rbac.operations`.
	// Kept only so startup can say it is ignored: a list maintained by hand
	// beside a list maintained by the compiler drifts, and every way it drifts
	// is quiet. See Operations.
	Declared []string            `yaml:"operations" json:"-"`
	Roles    map[string]RoleSpec `yaml:"roles" json:"roles"`
}

// Operation names. The endpoints are gated on these constants, so this is what
// the code actually checks — which is why the list a role may use is derived
// from here rather than written out in values.
const (
	OpLogs          = "logs"
	OpDescribe      = "describe"
	OpMetrics       = "metrics"
	OpPodRestart    = "pod-restart"
	OpHelmList      = "helm-list"
	OpHelmHistory   = "helm-history"
	OpHelmRollback  = "helm-rollback"
	OpHelmUninstall = "helm-uninstall"
	OpSecretList    = "secret-list"
	OpSecretRead    = "secret-read"
	OpSecretCreate  = "secret-create"
	OpSecretUpdate  = "secret-update"
	OpSecretDelete  = "secret-delete"

	// Not tied to a namespace: granted through RoleSpec.Global.
	OpUserList   = "user-list"
	OpUserManage = "user-manage"

	// Granted per configuration path through RoleSpec.Configs.
	OpConfigRead  = "config-read"
	OpConfigWrite = "config-write"

	// Building an image and putting a version into an environment are
	// different acts, permitted separately.
	OpImageRebuild   = "image-rebuild"
	OpPipelineDeploy = "pipeline-deploy"
)

// Operations returns the names a role may be granted in this deployment.
//
// Derived, not configured. A hand-written list beside the constants above
// drifts in two directions and both are quiet: a name in the list that the code
// never checks becomes a grant that does nothing, and a name the code checks
// but the list omits cannot be granted through the portal while grants already
// stored keep working. Neither shows up as an error anywhere.
//
// The optional features are left out when they are off, so the role editor does
// not offer permissions for a page that does not exist.
func Operations(c *Config) []string {
	ops := []string{
		OpLogs, OpDescribe, OpMetrics, OpPodRestart,
		OpHelmList, OpHelmHistory, OpHelmRollback, OpHelmUninstall,
		OpSecretList, OpSecretRead, OpSecretCreate, OpSecretUpdate, OpSecretDelete,
		OpUserList, OpUserManage,
	}
	if c.Configs.Enabled() {
		ops = append(ops, OpConfigRead, OpConfigWrite)
	}
	if c.ImageRebuild.Enabled {
		ops = append(ops, OpImageRebuild, OpPipelineDeploy)
	}
	return ops
}

// RoleSpec is what a single role may do.
type RoleSpec struct {
	// Description is shown in the access control UI.
	Description string           `yaml:"description" json:"description"`
	Namespaces  []NamespaceGrant `yaml:"namespaces" json:"namespaces"`
	// Global grants operations that belong to no namespace, such as seeing who
	// has access. A namespace grant of "*" does not imply these: reaching every
	// namespace and administering the portal itself are different powers, and
	// conflating them would hand the second to everyone who was given the first.
	Global []string `yaml:"global" json:"global"`
	// Configs grants access to service configuration by path. Kept apart from
	// Namespaces because the two do not line up: a configuration folder is named
	// after a service group, a namespace after a deployment of it, and in
	// practice the names differ.
	Configs []ConfigGrant `yaml:"configs" json:"configs"`
}

// ConfigGrant grants operations on one configuration path, or on all of them
// ("*"). A grant covers the path and everything under it.
type ConfigGrant struct {
	Path       string   `yaml:"path" json:"path"`
	Operations []string `yaml:"operations" json:"operations"`
}

// NamespaceGrant grants operations in one namespace, or in all of them ("*").
type NamespaceGrant struct {
	Namespace  string   `yaml:"namespace" json:"namespace"`
	Operations []string `yaml:"operations" json:"operations"`
}

// Validate ensures every referenced operation is declared.
func (r *RBACConfig) Validate() error {
	known := make(map[string]bool, len(r.Operations))
	for _, op := range r.Operations {
		known[op] = true
	}
	for role, spec := range r.Roles {
		for _, op := range spec.Global {
			if op != "*" && !known[op] {
				return fmt.Errorf("role %q: unknown global operation %q", role, op)
			}
		}
		for _, g := range spec.Configs {
			if g.Path == "" {
				return fmt.Errorf("role %q: a configuration grant has an empty path", role)
			}
			for _, op := range g.Operations {
				if op != "*" && !known[op] {
					return fmt.Errorf("role %q, configuration %q: unknown operation %q", role, g.Path, op)
				}
			}
		}
		for _, g := range spec.Namespaces {
			if g.Namespace == "" {
				return fmt.Errorf("role %q: a namespace grant has an empty namespace", role)
			}
			for _, op := range g.Operations {
				if op == "*" {
					continue
				}
				if !known[op] {
					return fmt.Errorf("role %q, namespace %q: unknown operation %q", role, g.Namespace, op)
				}
			}
		}
	}
	return nil
}
