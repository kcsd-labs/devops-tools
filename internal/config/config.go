// Package config loads the application and RBAC configuration from YAML.
package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
	"gopkg.in/yaml.v3"
)

// Config is the root configuration of a single instance. One instance serves
// exactly one Kubernetes cluster — the one it runs in.
type Config struct {
	// Environment is a free-form label ("dev", "staging", "production").
	// Shown in the UI and recorded in the audit log.
	Environment   string              `yaml:"environment"`
	Server        ServerConfig        `yaml:"server"`
	Auth          AuthConfig          `yaml:"auth"`
	Storage       StorageConfig       `yaml:"storage"`
	UI            UIConfig            `yaml:"ui"`
	Cluster       ClusterConfig       `yaml:"cluster"`
	Impersonation ImpersonationConfig `yaml:"impersonation"`
	Metrics       MetricsConfig       `yaml:"metrics"`
	Logs          LogsConfig          `yaml:"logs"`
	Configs       ConfigsConfig       `yaml:"configurations"`
	ImageRebuild  ImageRebuildConfig  `yaml:"imageRebuild"`

	// RBAC seeds the access model on the very first start. One file rather than
	// two: the roles it describes are configuration like everything else here,
	// and a second file bought nothing but a second thing to find.
	RBAC RBACConfig `yaml:"rbac"`
}

// ImageRebuildConfig turns on rebuilding a workload's image from the portal.
//
// Off by default and left out of the documentation deliberately. It only works
// where the image tag encodes the pipeline that produced it and the image path
// matches the GitLab project — a house convention, not something any
// installation has by default. Where those hold it is worth a great deal: an
// image removed by a registry retention policy can be brought back without
// anyone hunting for the pipeline that built it.
type ImageRebuildConfig struct {
	Enabled bool `yaml:"enabled"`
	// URL and token: empty falls back to the configuration repository's GitLab,
	// which is usually the same one.
	URL   string `yaml:"url"`
	Token string `yaml:"-"`
	// Environments are the tag prefixes that name an environment. A tag
	// starting with anything else is refused rather than interpreted — it
	// belongs to something that is not ours, and acting on it would retry a
	// stranger's pipeline.
	Environments []string `yaml:"environments"`
	// BuildJobs are the job names to look for, {env} substituted, tried in
	// order. Two are usual while a deployment migrates between builders.
	BuildJobs []string `yaml:"buildJobs"`
	// DeployJobs are the manual jobs the portal may release, {env}
	// substituted. Named exactly, never matched by shape: a manual job is
	// manual for a reason, and starting one because its name happened to
	// contain "deploy" is the sort of guess that puts a version into an
	// environment nobody meant to touch. Anything not listed is reported and
	// left for a person.
	DeployJobs []string `yaml:"deployJobs"`
	// Branches maps an environment to the branch a fresh pipeline runs on, for
	// when the original pipeline is gone.
	Branches map[string]string `yaml:"branches"`
}

func (c ImageRebuildConfig) Validate() error {
	if !c.Enabled {
		return nil
	}
	if c.URL == "" {
		return fmt.Errorf("imageRebuild.url is required (or configurations.git.url, which it falls back to)")
	}
	if c.Token == "" {
		return fmt.Errorf("imageRebuild is enabled but GITLAB_TOKEN is not set")
	}
	if len(c.Environments) == 0 {
		return fmt.Errorf("imageRebuild.environments is required: name the tag prefixes that identify " +
			"an environment, or every image tag will be refused")
	}
	if len(c.BuildJobs) == 0 {
		return fmt.Errorf("imageRebuild.buildJobs is required: name the job that builds the image")
	}
	for _, tpl := range c.BuildJobs {
		if !strings.Contains(tpl, "{env}") {
			// Not fatal in principle, but a job name shared by every
			// environment would retry the wrong one, and that is worth
			// refusing rather than discovering.
			return fmt.Errorf("imageRebuild.buildJobs entry %q has no {env}; "+
				"without it the same job is retried whatever the environment", tpl)
		}
	}
	for _, env := range c.Environments {
		if c.Branches[env] == "" {
			return fmt.Errorf("imageRebuild.branches has no branch for environment %q, "+
				"so a fresh pipeline could not be started when the original is gone", env)
		}
	}
	for _, tpl := range c.DeployJobs {
		if !strings.Contains(tpl, "{env}") {
			return fmt.Errorf("imageRebuild.deployJobs entry %q has no {env}; "+
				"without it the same job is released whatever the environment", tpl)
		}
	}
	// deployJobs may be empty on purpose: an installation that rebuilds images
	// but never releases anything from the portal simply lists none, and the
	// deploy half stays inert.
	return nil
}

type ServerConfig struct {
	Addr       string `yaml:"addr"`
	CORSOrigin string `yaml:"corsOrigin"`
}

// AuthConfig selects and configures the authentication provider.
//
//	local — users defined in this config (bcrypt hashes); no external IdP needed
//	ldap  — bind against Active Directory / OpenLDAP, groups map to roles
//	oidc  — an external identity provider issues the tokens (Keycloak, Dex, Okta…)
//
// With local and ldap the application issues its own session tokens; with oidc
// it only verifies tokens issued by the provider.
type AuthConfig struct {
	Provider  string          `yaml:"provider"`
	Session   SessionConfig   `yaml:"session"`
	Local     LocalAuthConfig `yaml:"local"`
	LDAP      LDAPConfig      `yaml:"ldap"`
	OIDC      OIDCConfig      `yaml:"oidc"`
	Bootstrap BootstrapConfig `yaml:"bootstrapAdmins"`
}

// Provider kinds.
const (
	ProviderLocal = "local"
	ProviderLDAP  = "ldap"
	ProviderOIDC  = "oidc"
)

// SessionConfig controls the session tokens issued for the local and ldap
// providers. SigningKey never comes from the YAML file — see the env override.
type SessionConfig struct {
	TTL        time.Duration `yaml:"ttl"`
	SigningKey string        `yaml:"-"`
}

// LocalAuthConfig holds users defined directly in the configuration.
// Intended for evaluation, small teams and air-gapped installations.
type LocalAuthConfig struct {
	Users []LocalUser `yaml:"users"`
}

type LocalUser struct {
	Username string   `yaml:"username"`
	Email    string   `yaml:"email"`
	Roles    []string `yaml:"roles"`
	// PasswordHash is a bcrypt hash; generate with `devops-tools hash-password`
	// or `htpasswd -bnBC 10 "" <password> | tr -d ':\n'`.
	PasswordHash string `yaml:"passwordHash"`
}

// LDAPConfig describes how to authenticate against an LDAP directory and how
// to turn directory groups into application roles.
type LDAPConfig struct {
	// URL of the directory, e.g. ldaps://ldap.example.com:636.
	// Plain ldap:// is refused unless InsecureAllowPlaintext is set.
	URL string `yaml:"url"`
	// BindDN is the service account used to search for users. Leave empty for
	// anonymous search. The password comes from the environment, never YAML.
	BindDN       string `yaml:"bindDN"`
	BindPassword string `yaml:"-"`

	UserSearchBase string `yaml:"userSearchBase"`
	// UserFilter must contain a single %s placeholder for the username,
	// e.g. (sAMAccountName=%s) for Active Directory or (uid=%s) for OpenLDAP.
	UserFilter string `yaml:"userFilter"`
	// EmailAttribute names the attribute holding the address (default: mail).
	EmailAttribute string `yaml:"emailAttribute"`

	// GroupSearchBase and GroupFilter locate the groups a user belongs to.
	// GroupFilter may contain %s (user DN) and %u (username), e.g. (member=%s).
	// When both are empty, groups are read from the user's memberOf attribute.
	GroupSearchBase string `yaml:"groupSearchBase"`
	GroupFilter     string `yaml:"groupFilter"`
	// GroupNameAttribute names the attribute used as the group name (default: cn).
	GroupNameAttribute string `yaml:"groupNameAttribute"`

	// GroupMapping and DefaultRoles no longer grant anything: roles are held in
	// the portal and assigned there. The fields are kept so that a configuration
	// written for an earlier build is refused with an explanation rather than
	// silently ignored — see Validate. Groups themselves are still read, and
	// shown against each user as the context for granting a role.
	GroupMapping map[string][]string `yaml:"groupMapping"`
	DefaultRoles []string            `yaml:"defaultRoles"`

	TLS LDAPTLSConfig `yaml:"tls"`
}

type LDAPTLSConfig struct {
	// StartTLS upgrades a plain ldap:// connection instead of using ldaps://.
	StartTLS bool `yaml:"startTLS"`
	// CAFile is a PEM bundle used to verify the directory certificate.
	CAFile string `yaml:"caFile"`
	// InsecureSkipVerify disables certificate verification. Never use in production.
	InsecureSkipVerify bool `yaml:"insecureSkipVerify"`
	// InsecureAllowPlaintext permits ldap:// without StartTLS. Never use in production.
	InsecureAllowPlaintext bool `yaml:"insecureAllowPlaintext"`
}

// OIDCConfig points at an external identity provider.
type OIDCConfig struct {
	IssuerURL string `yaml:"issuerURL"`
	ClientID  string `yaml:"clientID"`
	// DisplayName is what the sign-in button offers to sign in with: the name
	// people know the provider by, rather than the issuer URL. Defaults to
	// "single sign-on", which says what it does without guessing at a product.
	DisplayName string `yaml:"displayName"`
	// SkipAudienceCheck accepts tokens whose audience is not this client.
	// Keycloak leaves the audience of a public client's token empty, so this is
	// normally on there — which is what makes the azp check below matter.
	SkipAudienceCheck bool `yaml:"skipAudienceCheck"`
	// RequireAuthorizedParty rejects a token whose azp claim names a different
	// client. Without it, and with the audience unchecked, every token the realm
	// signs is accepted — including ones issued to other applications in it,
	// which is a decision the identity provider is supposed to make per client.
	//
	// On unless set to false. azp is not required by the OIDC specification, so
	// a provider that omits it needs this turned off; the error says as much.
	RequireAuthorizedParty *bool `yaml:"requireAuthorizedParty"`
	// RolesClaim no longer grants anything; see the LDAP GroupMapping field.
	// Kept so that a configuration written for an earlier build is refused with
	// an explanation rather than silently ignored.
	RolesClaim string `yaml:"rolesClaim"`
	// GroupMapping no longer grants anything; see the LDAP field of the same name.
	GroupMapping map[string][]string `yaml:"groupMapping"`
}

// RequiresAuthorizedParty reports the effective setting: on unless explicitly
// disabled. A method rather than a plain field so it holds even for a config
// built in a test, without going through applyDefaults.
func (o OIDCConfig) RequiresAuthorizedParty() bool {
	return o.RequireAuthorizedParty == nil || *o.RequireAuthorizedParty
}

// BootstrapConfig is the grant that the portal's own model cannot take away.
//
// Every other role is assigned in the UI and stored on the volume, which leaves
// one way to fail badly: a mistake there, or a lost volume, and nobody can sign
// in to fix it. These users always hold these roles, and only a change to the
// deployment can alter that.
type BootstrapConfig struct {
	Users []string `yaml:"users"`
	Roles []string `yaml:"roles"`
	// PasswordLogin is the first administrator's own credentials, usable
	// whatever the provider is. Optional, and off unless a hash is set.
	PasswordLogin BootstrapLogin `yaml:"passwordLogin"`
}

// BootstrapLogin is a password sign-in that does not go through the configured
// provider.
//
// It exists for the two moments the provider cannot help: before anyone has
// been given access, and when the provider itself is unreachable. That also
// makes it a way into the portal that a directory cannot revoke, so it is off
// unless deliberately configured, announced at startup, and reachable only at
// its own address rather than from the ordinary sign-in screen.
type BootstrapLogin struct {
	Username string `yaml:"username"`
	// PasswordHash is bcrypt, as for a local user. Prefer supplying it through
	// DEVOPS_TOOLS_BOOTSTRAP_PASSWORD_HASH: it is a credential, and the YAML
	// normally lives in a ConfigMap.
	PasswordHash string `yaml:"passwordHash"`
}

// Enabled reports whether the break-glass sign-in is configured.
func (b BootstrapLogin) Enabled() bool { return b.Username != "" && b.PasswordHash != "" }

// Grants reports whether username is a bootstrap administrator. Matching is
// case-insensitive, as it is everywhere else usernames are compared.
func (b BootstrapConfig) Grants(username string) bool {
	if len(b.Roles) == 0 {
		return false
	}
	for _, u := range b.Users {
		if strings.EqualFold(strings.TrimSpace(u), strings.TrimSpace(username)) {
			return true
		}
	}
	return false
}

// UIConfig is what the interface calls itself.
//
// The name is a setting rather than a string in the code because an
// installation elsewhere will want its own — and because the technical
// identity (module, chart, image, environment variables, the volume) has to
// stay put regardless: renaming that would rename the PersistentVolumeClaim,
// and the access model with it.
type UIConfig struct {
	// BrandName appears in the sidebar, on the sign-in screen and in the
	// browser tab. Defaults to DevOps Tools.
	BrandName string `yaml:"brandName"`
	// BrandInitials is the two-letter mark on the logo. Derived from BrandName
	// when empty: the first letters of the first two words, or the first two
	// letters of a single one.
	BrandInitials string `yaml:"brandInitials"`
}

// Initials returns the mark to draw, derived when not set explicitly.
func (u UIConfig) Initials() string {
	if u.BrandInitials != "" {
		return strings.ToUpper(u.BrandInitials)
	}
	words := strings.Fields(u.BrandName)
	switch {
	case len(words) >= 2:
		return strings.ToUpper(string([]rune(words[0])[:1]) + string([]rune(words[1])[:1]))
	case len(words) == 1 && len([]rune(words[0])) >= 2:
		return strings.ToUpper(string([]rune(words[0])[:2]))
	}
	return "DT"
}

// StorageConfig is where the runtime-editable part of the access model lives.
type StorageConfig struct {
	// Path is a JSON file, normally on a persistent volume. Losing it loses
	// every role assignment made in the UI.
	Path string `yaml:"path"`
}

// ClusterConfig selects how to reach the Kubernetes API.
type ClusterConfig struct {
	// InCluster uses the pod's ServiceAccount (the normal deployment mode).
	InCluster bool `yaml:"inCluster"`
	// Kubeconfig is used when InCluster is false (local development).
	Kubeconfig string `yaml:"kubeconfig"`
	// HiddenNamespaces are kept out of the list a role granted "*" is shown.
	// They are only hidden, not forbidden: a wildcard role still carries the
	// permission, and opening one by name works. Entries are exact names or a
	// prefix ending in `*`. Unset means DefaultHiddenNamespaces.
	HiddenNamespaces []string `yaml:"hiddenNamespaces"`
}

// DefaultHiddenNamespaces keeps the cluster's own plumbing out of a portal
// aimed at application teams. Set cluster.hiddenNamespaces to [] to see it.
var DefaultHiddenNamespaces = []string{"kube-*", "default"}

// ImpersonationConfig makes the API server see the end user rather than the
// application's ServiceAccount, so Kubernetes RBAC applies as a second gate.
type ImpersonationConfig struct {
	Enabled      bool   `yaml:"enabled"`
	DefaultGroup string `yaml:"defaultGroup"`
}

// MetricsConfig points at Prometheus for the pod metrics tab. Empty disables it.
type MetricsConfig struct {
	PrometheusURL string `yaml:"prometheusURL"`
}

// LogsConfig points at Loki for historical logs. Empty leaves only live
// Kubernetes logs available.
type LogsConfig struct {
	LokiURL string `yaml:"lokiURL"`
}

// ConfigsConfig turns on the Configurations page and says where the service
// configuration lives. With nothing enabled the page does not exist and the
// endpoints answer 404, which is the state an installation that does not manage
// configuration should be left in.
type ConfigsConfig struct {
	Consul ConsulSourceConfig `yaml:"consul"`
	Git    GitSourceConfig    `yaml:"git"`
}

// GitSourceConfig addresses one branch of one GitLab repository.
//
// Where a synchroniser copies that repository into Consul, this is the end to
// edit: a change written straight to Consul is undone the next time the
// synchroniser runs.
type GitSourceConfig struct {
	Enabled bool   `yaml:"enabled"`
	URL     string `yaml:"url"`
	Project string `yaml:"project"`
	// Branch is the environment's branch. One instance serves one branch —
	// an instance that could write to several would be a way to edit
	// production from the development portal.
	Branch string `yaml:"branch"`
	// BasePath is the folder inside the repository the paths are relative to.
	// Empty means the repository root.
	BasePath string `yaml:"basePath"`
	// Token comes from GITLAB_TOKEN, never from the configuration file.
	Token string `yaml:"-"`
}

func (g GitSourceConfig) Validate() error {
	if !g.Enabled {
		return nil
	}
	if g.URL == "" {
		return fmt.Errorf("configurations.git.url is required")
	}
	if g.Project == "" {
		return fmt.Errorf("configurations.git.project is required, e.g. group/subgroup/configuration")
	}
	if g.Branch == "" {
		// Defaulting to something plausible would eventually commit to the
		// wrong branch of the right repository, which is worse than not starting.
		return fmt.Errorf("configurations.git.branch is required: name the branch this environment reads and writes")
	}
	if g.Token == "" {
		return fmt.Errorf("configurations.git is enabled but GITLAB_TOKEN is not set; " +
			"put a project access token with the api scope in a Secret and list it under envFromSecrets")
	}
	return nil
}

// ConsulSourceConfig addresses one Consul key/value store.
type ConsulSourceConfig struct {
	Enabled bool   `yaml:"enabled"`
	Address string `yaml:"address"`
	// Prefix bounds what the portal can see and write. It is not a security
	// boundary on its own — the token should be scoped to the same prefix, so
	// that a mistake here cannot reach the rest of the store.
	Prefix string `yaml:"prefix"`
}

// Enabled reports whether any configuration source is switched on.
func (c ConfigsConfig) Enabled() bool { return c.Consul.Enabled || c.Git.Enabled }

// Validate checks the sources, and refuses the combination that is not built
// yet rather than quietly serving one of the two.
func (c ConfigsConfig) Validate() error {
	if err := c.Consul.Validate(); err != nil {
		return err
	}
	if err := c.Git.Validate(); err != nil {
		return err
	}
	return nil
}

// Paired reports whether both sides are configured: the repository is the
// source of truth and Consul is brought up to date after each commit, rather
// than at the synchroniser's next pass.
func (c ConfigsConfig) Paired() bool { return c.Consul.Enabled && c.Git.Enabled }

func (c ConsulSourceConfig) Validate() error {
	if !c.Enabled {
		return nil
	}
	if c.Prefix == "" {
		// An empty prefix is the whole key/value store. That is unlikely to be
		// meant, and the damage from getting it wrong is not recoverable from
		// the portal.
		return fmt.Errorf("configurations.consul.prefix is required: an empty prefix would expose the entire key/value store")
	}
	return nil
}

// Load reads application.yaml from the given source.
func Load(loader Loader) (*Config, *RBACConfig, error) {
	cfg := &Config{}
	if err := readYAML(loader, "application.yaml", cfg); err != nil {
		return nil, nil, fmt.Errorf("load app config: %w", err)
	}
	applyEnvOverrides(cfg)
	if err := applyBootstrapPassword(cfg); err != nil {
		return nil, nil, err
	}
	applyDefaults(cfg)
	if err := cfg.Validate(); err != nil {
		return nil, nil, fmt.Errorf("invalid config: %w", err)
	}

	// The vocabulary comes from the code, and the roles are validated against
	// it — so a seeded role naming an operation this build does not implement
	// stops the service here, rather than becoming a grant nothing ever checks.
	rbac := cfg.RBAC
	rbac.Operations = Operations(cfg)
	if err := rbac.Validate(); err != nil {
		return nil, nil, fmt.Errorf("rbac config: %w", err)
	}
	return cfg, &rbac, nil
}

func applyDefaults(c *Config) {
	if c.Server.Addr == "" {
		c.Server.Addr = ":8080"
	}
	if c.Environment == "" {
		c.Environment = "unknown"
	}
	if c.Auth.Provider == "" {
		c.Auth.Provider = ProviderLocal
	}
	if c.Auth.Session.TTL == 0 {
		c.Auth.Session.TTL = 8 * time.Hour
	}
	if c.Auth.LDAP.UserFilter == "" {
		c.Auth.LDAP.UserFilter = "(uid=%s)"
	}
	if c.Auth.LDAP.EmailAttribute == "" {
		c.Auth.LDAP.EmailAttribute = "mail"
	}
	if c.Auth.LDAP.GroupNameAttribute == "" {
		c.Auth.LDAP.GroupNameAttribute = "cn"
	}
	// nil is "not configured"; an explicit [] is "hide nothing", so only the
	// former gets the defaults.
	if c.Cluster.HiddenNamespaces == nil {
		c.Cluster.HiddenNamespaces = DefaultHiddenNamespaces
	}
	if c.Storage.Path == "" {
		c.Storage.Path = "/data/devops-tools.json"
	}
	if c.Auth.OIDC.DisplayName == "" {
		c.Auth.OIDC.DisplayName = "single sign-on"
	}
	if c.UI.BrandName == "" {
		c.UI.BrandName = "DevOps Tools"
	}
}

// bootstrapPasswordMinLength matches what the UI asks of any other password.
// A portal that can read secrets and restart workloads should not be reachable
// with four characters.
const bootstrapPasswordMinLength = 8

// applyBootstrapPassword turns a plaintext break-glass password into a hash.
//
// Supplying the password itself is the ordinary way to configure this: asking
// for a bcrypt hash means running a second command before the first install,
// and the value ends up in the same Kubernetes Secret either way. Hashing it
// here keeps what that buys — the stored value is not something that can be
// replayed as a credential, or tried against whatever else shares the password.
//
// Plaintext is accepted from the environment only, never from the YAML: that
// file becomes a ConfigMap, which is a different thing to be able to read.
func applyBootstrapPassword(c *Config) error {
	pw := os.Getenv("DEVOPS_TOOLS_BOOTSTRAP_PASSWORD")
	if pw == "" {
		return nil
	}
	if c.Auth.Bootstrap.PasswordLogin.PasswordHash != "" {
		// Both given. Rather than pick one silently and leave whoever set the
		// other wondering why their password does not work, refuse.
		return fmt.Errorf("both DEVOPS_TOOLS_BOOTSTRAP_PASSWORD and a passwordHash are set; " +
			"supply one or the other")
	}
	if len(pw) < bootstrapPasswordMinLength {
		return fmt.Errorf("DEVOPS_TOOLS_BOOTSTRAP_PASSWORD is shorter than %d characters",
			bootstrapPasswordMinLength)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("hash DEVOPS_TOOLS_BOOTSTRAP_PASSWORD: %w", err)
	}
	c.Auth.Bootstrap.PasswordLogin.PasswordHash = string(hash)
	return nil
}

// errRolesFromGroups explains where roles come from now. It is a startup error
// rather than a warning: an operator who wrote a group mapping expects it to
// grant access, and ignoring it would hand out less access than they think.
const errRolesFromGroups = "roles are no longer derived from directory groups — they are assigned in the portal, " +
	"under Users, and kept on the storage volume. Remove %s. " +
	"Groups are still read and shown next to each user. " +
	"To grant the first administrator, use auth.bootstrapAdmins"

// bcryptAlphabet is the base64 variant bcrypt encodes its salt and digest with.
// It has no `=` padding, which is what makes hashes copied from tutorials —
// where the padding was never stripped — quietly unusable.
const bcryptAlphabet = "./ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

// shippedHash is the hash of "admin" that the chart's default values carry, so
// that a fresh install can be signed into without preparing anything.
//
// It is in a public repository, which makes it a published password rather than
// a weak one. Recognising it lets the service say so at every start: the values
// file shouts about it in a comment, and a comment is not read by whoever
// inherits the deployment two years later.
const shippedHash = "$2a$10$g8J.N1mZm6FUHfQR5FMYButGpMFIZeYtnd4aJd6sp.gXQQ.LQmUDq"

// UsesShippedPassword reports whether any local account still has the example
// password from the chart's defaults.
func (c *Config) UsesShippedPassword() []string {
	var users []string
	for _, u := range c.Auth.Local.Users {
		if u.PasswordHash == shippedHash {
			users = append(users, u.Username)
		}
	}
	if c.Auth.Bootstrap.PasswordLogin.PasswordHash == shippedHash {
		users = append(users, c.Auth.Bootstrap.PasswordLogin.Username)
	}
	return users
}

// checkBcryptHash reports whether a password will ever be able to match h.
//
// Neither of the obvious checks is enough. bcrypt.Cost reads only the version
// and cost prefix. CompareHashAndPassword is no better: bcrypt never decodes
// the stored digest, it re-hashes the candidate and compares the two as raw
// bytes, so a digest holding characters outside the alphabet is not a decoding
// error — it is simply a hash nothing matches. At login that is impossible to
// tell apart from a mistyped password, so it has to be caught here instead.
func checkBcryptHash(h string) error {
	if _, err := bcrypt.Cost([]byte(h)); err != nil {
		return fmt.Errorf("passwordHash is not a bcrypt hash (%v)", err)
	}
	// bcrypt.Cost has validated the `$2a$10$` prefix, so the digest is whatever
	// follows the last separator: 22 characters of salt and 31 of hash.
	body := h[strings.LastIndexByte(h, '$')+1:]
	if len(body) != 53 {
		return fmt.Errorf("passwordHash is truncated: %d characters after the cost, expected 53", len(body))
	}
	for _, r := range body {
		if !strings.ContainsRune(bcryptAlphabet, r) {
			return fmt.Errorf("passwordHash contains %q, which bcrypt never emits, "+
				"so no password can match it", r)
		}
	}
	return nil
}

// Validate catches misconfiguration at startup rather than on first login.
func (c *Config) Validate() error {
	switch c.Auth.Provider {
	case ProviderLocal:
		// An empty user list used to be refused outright, which forced every
		// installation to start with a bcrypt hash written into values — and
		// meant the chart had to ship one to be installable at all. The
		// break-glass account covers that first sign-in now: it works whatever
		// the provider is, takes a plain password, and can create the rest of
		// the accounts from the UI. So the list may be empty, as long as
		// something can get in.
		if len(c.Auth.Local.Users) == 0 && !c.Auth.Bootstrap.PasswordLogin.Enabled() {
			return fmt.Errorf("auth.provider=local and nothing can sign in: define auth.local.users, " +
				"or give auth.bootstrapAdmins.passwordLogin a username and a password and create " +
				"the accounts from the portal")
		}
		for i, u := range c.Auth.Local.Users {
			if u.Username == "" {
				return fmt.Errorf("auth.local.users[%d]: username is required", i)
			}
			if u.PasswordHash == "" {
				return fmt.Errorf("auth.local.users[%d] (%s): passwordHash is required", i, u.Username)
			}
			if err := checkBcryptHash(u.PasswordHash); err != nil {
				return fmt.Errorf("auth.local.users[%d] (%s): %w; "+
					"generate one with `devops-tools hash-password <password>`", i, u.Username, err)
			}
		}
	case ProviderLDAP:
		if c.Auth.LDAP.URL == "" {
			return fmt.Errorf("auth.provider=ldap but auth.ldap.url is empty")
		}
		if c.Auth.LDAP.UserSearchBase == "" {
			return fmt.Errorf("auth.provider=ldap but auth.ldap.userSearchBase is empty")
		}
		if !strings.Contains(c.Auth.LDAP.UserFilter, "%s") {
			return fmt.Errorf("auth.ldap.userFilter must contain %%s for the username")
		}
		plain := strings.HasPrefix(c.Auth.LDAP.URL, "ldap://")
		if plain && !c.Auth.LDAP.TLS.StartTLS && !c.Auth.LDAP.TLS.InsecureAllowPlaintext {
			return fmt.Errorf("auth.ldap.url uses plaintext ldap://; enable tls.startTLS or use ldaps://")
		}
		if len(c.Auth.LDAP.GroupMapping) > 0 {
			return fmt.Errorf(errRolesFromGroups, "auth.ldap.groupMapping")
		}
		if len(c.Auth.LDAP.DefaultRoles) > 0 {
			return fmt.Errorf(errRolesFromGroups, "auth.ldap.defaultRoles")
		}
	case ProviderOIDC:
		if c.Auth.OIDC.IssuerURL == "" {
			return fmt.Errorf("auth.provider=oidc but auth.oidc.issuerURL is empty")
		}
		if c.Auth.OIDC.ClientID == "" {
			return fmt.Errorf("auth.provider=oidc but auth.oidc.clientID is empty")
		}
		if len(c.Auth.OIDC.GroupMapping) > 0 {
			return fmt.Errorf(errRolesFromGroups, "auth.oidc.groupMapping")
		}
		if c.Auth.OIDC.RolesClaim != "" {
			return fmt.Errorf(errRolesFromGroups, "auth.oidc.rolesClaim")
		}
		if len(c.Auth.Bootstrap.Users) == 0 && !c.Auth.Bootstrap.PasswordLogin.Enabled() {
			// With no roles coming from the provider, a fresh installation has
			// nobody who can grant the first one. Saying so at startup beats
			// discovering it from an empty portal after signing in.
			return fmt.Errorf("auth.provider=oidc grants no roles by itself — they are assigned in the " +
				"portal, under Access management. Set auth.bootstrapAdmins.users to the " +
				"preferred_username of whoever should administer it, or nobody will be able to")
		}
	default:
		return fmt.Errorf("unknown auth.provider %q (expected local, ldap or oidc)", c.Auth.Provider)
	}

	if len(c.Auth.Bootstrap.Users) > 0 && len(c.Auth.Bootstrap.Roles) == 0 {
		return fmt.Errorf("auth.bootstrapAdmins.users is set but auth.bootstrapAdmins.roles is empty, " +
			"so it grants nothing")
	}
	if l := c.Auth.Bootstrap.PasswordLogin; l.Username != "" || l.PasswordHash != "" {
		if l.Username == "" {
			return fmt.Errorf("auth.bootstrapAdmins.passwordLogin.passwordHash is set but username is empty")
		}
		if l.PasswordHash == "" {
			return fmt.Errorf("auth.bootstrapAdmins.passwordLogin.username is set but no password " +
				"was supplied; set DEVOPS_TOOLS_BOOTSTRAP_PASSWORD to the password itself, or " +
				"passwordHash / DEVOPS_TOOLS_BOOTSTRAP_PASSWORD_HASH to a bcrypt hash of it")
		}
		if err := checkBcryptHash(l.PasswordHash); err != nil {
			return fmt.Errorf("auth.bootstrapAdmins.passwordLogin: %w; "+
				"generate one with `devops-tools hash-password <password>`", err)
		}
		// Signing in and being allowed to do anything are separate. Without the
		// name in Users this account authenticates and then finds an empty
		// portal — which is not what anyone configuring it intends.
		if !c.Auth.Bootstrap.Grants(l.Username) {
			return fmt.Errorf("auth.bootstrapAdmins.passwordLogin.username %q is not listed in "+
				"auth.bootstrapAdmins.users, so it could sign in with no access at all", l.Username)
		}
	}
	if err := c.Configs.Validate(); err != nil {
		return err
	}
	if err := c.ImageRebuild.Validate(); err != nil {
		return err
	}
	return nil
}

// applyEnvOverrides lets deployments override individual fields without
// rewriting the YAML. Secrets are only ever read from the environment.
func applyEnvOverrides(c *Config) {
	// secrets — environment only, never stored in the config file
	c.Auth.Session.SigningKey = os.Getenv("DEVOPS_TOOLS_SESSION_KEY")
	c.Auth.LDAP.BindPassword = os.Getenv("DEVOPS_TOOLS_LDAP_BIND_PASSWORD")
	// GITLAB_TOKEN rather than a DEVOPS_TOOLS_ name: it is the variable the
	// GitLab tooling already uses, and the same Secret often serves both.
	c.Configs.Git.Token = os.Getenv("GITLAB_TOKEN")
	// Its own variable, falling back to the shared one.
	//
	// The two need quite different reach: committing configuration touches one
	// repository, while rebuilding images touches every project that deploys
	// here. One token for both means the narrow job is done with the wide
	// token — and a token that can commit to every application repository is a
	// long way from one that can commit to the configuration.
	c.ImageRebuild.Token = os.Getenv("DEVOPS_TOOLS_IMAGE_REBUILD_TOKEN")
	if c.ImageRebuild.Token == "" {
		c.ImageRebuild.Token = os.Getenv("GITLAB_TOKEN")
	}
	if c.ImageRebuild.URL == "" {
		// Usually the same GitLab as the configuration repository, so it need
		// not be written twice.
		c.ImageRebuild.URL = c.Configs.Git.URL
	}
	if h := os.Getenv("DEVOPS_TOOLS_BOOTSTRAP_PASSWORD_HASH"); h != "" {
		c.Auth.Bootstrap.PasswordLogin.PasswordHash = h
	}

	set(&c.Environment, "DEVOPS_TOOLS_ENVIRONMENT")
	set(&c.Server.Addr, "DEVOPS_TOOLS_SERVER_ADDR")
	set(&c.Server.CORSOrigin, "DEVOPS_TOOLS_CORS_ORIGIN")
	set(&c.Auth.Provider, "DEVOPS_TOOLS_AUTH_PROVIDER")
	set(&c.Auth.OIDC.IssuerURL, "DEVOPS_TOOLS_OIDC_ISSUER_URL")
	set(&c.Auth.OIDC.ClientID, "DEVOPS_TOOLS_OIDC_CLIENT_ID")
	set(&c.Auth.LDAP.URL, "DEVOPS_TOOLS_LDAP_URL")
	set(&c.Auth.LDAP.BindDN, "DEVOPS_TOOLS_LDAP_BIND_DN")
	set(&c.Metrics.PrometheusURL, "DEVOPS_TOOLS_PROMETHEUS_URL")
	set(&c.Logs.LokiURL, "DEVOPS_TOOLS_LOKI_URL")
	// The Consul token is not read here: the Consul client picks up the
	// standard CONSUL_HTTP_TOKEN itself, as the rest of the Consul tooling does.
	set(&c.Configs.Consul.Address, "DEVOPS_TOOLS_CONFIGS_CONSUL_ADDR")
	set(&c.Configs.Consul.Prefix, "DEVOPS_TOOLS_CONFIGS_CONSUL_PREFIX")
	set(&c.Configs.Git.URL, "DEVOPS_TOOLS_CONFIGS_GIT_URL")
	set(&c.Configs.Git.Project, "DEVOPS_TOOLS_CONFIGS_GIT_PROJECT")
	set(&c.Configs.Git.Branch, "DEVOPS_TOOLS_CONFIGS_GIT_BRANCH")
	set(&c.Configs.Git.BasePath, "DEVOPS_TOOLS_CONFIGS_GIT_BASE_PATH")
}

func set(field *string, env string) {
	if v := os.Getenv(env); v != "" {
		*field = v
	}
}

func readYAML(loader Loader, key string, out any) error {
	data, err := loader.Read(key)
	if err != nil {
		return err
	}
	return yaml.Unmarshal(data, out)
}
