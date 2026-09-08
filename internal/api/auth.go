package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"devops-tools/internal/audit"
	"devops-tools/internal/auth"
	"devops-tools/internal/version"
)

// handleAuthInfo tells the browser which authentication provider is active, so
// it can either render a login form or start an OIDC redirect. Unauthenticated
// by design — it exposes no user data.
func (s *Server) handleAuthInfo(w http.ResponseWriter, _ *http.Request) {
	info := s.auth.PublicInfo()
	// The sign-in screen needs the name before anyone has signed in, so it
	// travels with the provider information rather than with the user.
	writeJSON(w, http.StatusOK, struct {
		auth.PublicInfo
		BrandName     string `json:"brandName"`
		BrandInitials string `json:"brandInitials"`
	}{info, s.cfg.UI.BrandName, s.cfg.UI.Initials()})
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type loginResponse struct {
	Token     string     `json:"token"`
	ExpiresAt time.Time  `json:"expiresAt"`
	User      *auth.User `json:"user"`
}

// handleLogin verifies credentials against the configured provider and returns
// a session token. Only available for the local and ldap providers.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if !s.auth.SupportsLogin() {
		http.Error(w, "password login is not enabled", http.StatusNotFound)
		return
	}
	var body loginRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	token, expiresAt, user, err := s.auth.Login(r.Context(), body.Username, body.Password)

	// Record every attempt: failed logins are exactly what a security review
	// wants to see. The password itself is never logged.
	s.audit.Log(audit.Entry{
		User: body.Username, Environment: s.cfg.Environment, Namespace: "-",
		Operation: "login", Allowed: true, Success: err == nil, Error: errStr(err),
	})

	if err != nil {
		if errors.Is(err, auth.ErrTooManyAttempts) {
			http.Error(w, "too many failed attempts, try again later", http.StatusTooManyRequests)
			return
		}
		// Deliberately vague: do not reveal whether the account exists.
		http.Error(w, "invalid username or password", http.StatusUnauthorized)
		return
	}
	writeJSON(w, http.StatusOK, loginResponse{Token: token, ExpiresAt: expiresAt, User: user})
}

// handleMe reports who the caller is and which optional features are enabled,
// so the UI can hide what this deployment does not have.
func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.FromContext(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{
		"username": user.Username,
		"email":    user.Email,
		// Empty lists rather than null: a caller with no roles is the normal
		// state for someone who has just signed in, and every consumer would
		// otherwise need a guard for it.
		"roles":        nonNil(user.Roles),
		"environment":  s.cfg.Environment,
		"authProvider": s.auth.Kind(),
		"version":      version.Version,
		"groups":       nonNil(user.Groups),
		"capabilities": map[string]bool{
			"metrics": s.prom != nil,
			"loki":    s.loki != nil,
			// Unlike the others this depends on the caller, not the deployment:
			// the Users page is only in the menu for someone who may open it.
			"users":       s.authz.AllowedGlobal(user.Roles, OpUserList),
			"manageUsers": s.authz.AllowedGlobal(user.Roles, OpUserManage),
			// Both must hold: the deployment has a configuration source, and
			// this person was granted a path in it. Someone with no grant would
			// otherwise find the page and an empty list, which reads as a fault.
			"configurations": s.configs != nil && len(s.authz.VisibleConfigPaths(user.Roles)) > 0,
		},
	})
}

// namespaceList is what the namespace page renders.
//
// Wildcard is reported separately rather than inferred from the names, because
// the two carry different meaning. For a wildcard role the names are the
// cluster's current namespaces — a snapshot, not the extent of the permission —
// so the page keeps offering free-form entry for anything created since, or
// hidden by configuration. For every other role the names are the permission.
type namespaceList struct {
	Namespaces []string `json:"namespaces"`
	Wildcard   bool     `json:"wildcard"`
}

// handleNamespaces lists the namespaces the caller may open.
func (s *Server) handleNamespaces(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.FromContext(r.Context())
	visible := s.authz.VisibleNamespaces(user.Roles)

	if len(visible) == 1 && visible[0] == "*" {
		all, err := s.kube.ListNamespaces(r.Context(), user.Username, s.cfg.Cluster.HiddenNamespaces)
		if err != nil {
			// Losing the list is not losing the access: the role still grants
			// every namespace, so fall back to typing a name rather than
			// leaving the page empty. This is the expected path when the
			// ServiceAccount has no permission to list namespaces.
			slog.Warn("list namespaces for a wildcard role", "user", user.Username, "err", err)
			writeJSON(w, http.StatusOK, namespaceList{Namespaces: []string{}, Wildcard: true})
			return
		}
		writeJSON(w, http.StatusOK, namespaceList{Namespaces: all, Wildcard: true})
		return
	}
	writeJSON(w, http.StatusOK, namespaceList{Namespaces: visible, Wildcard: false})
}
