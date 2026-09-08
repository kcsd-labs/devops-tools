package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"
	"golang.org/x/crypto/bcrypt"

	"devops-tools/internal/audit"
	"devops-tools/internal/auth"
	"devops-tools/internal/config"
	"devops-tools/internal/store"
)

// minPasswordLength is a floor, not a policy. A portal that can restart
// workloads and read secrets should not be reachable with a four-character
// password, but anything more opinionated belongs to whoever runs it.
const minPasswordLength = 8

// logAccessChange records a change to who can do what. These are the entries a
// security review asks for first, so they are written even on failure.
func (s *Server) logAccessChange(r *http.Request, operation, target string, err error) {
	actor, _ := auth.FromContext(r.Context())
	name := ""
	if actor != nil {
		name = actor.Username
	}
	s.audit.Log(audit.Entry{
		User: name, Environment: s.cfg.Environment, Namespace: "-",
		Operation: operation, Target: target,
		Allowed: true, Success: err == nil, Error: errStr(err),
	})
}

// writeStoreError turns a store failure into the right status code.
func writeStoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
	case errors.Is(err, store.ErrExists):
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
	case errors.Is(err, store.ErrNotManaged):
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
	}
}

// --- users -----------------------------------------------------------------

type createUserRequest struct {
	Username string   `json:"username"`
	Email    string   `json:"email"`
	Password string   `json:"password"`
	Roles    []string `json:"roles"`
}

// handleCreateUser adds a local account. Only meaningful with the local
// provider: anywhere else the credentials belong to the directory, and an
// account created here could never be signed in to.
func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	if s.auth.Kind() != config.ProviderLocal {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "accounts are created in your identity provider, not here; " +
				"people appear in this list once they have signed in",
		})
		return
	}
	var body createUserRequest
	if err := decodeJSON(w, r, &body); err != nil {
		return
	}
	if len(body.Password) < minPasswordLength {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "the password is too short",
		})
		return
	}
	if err := s.checkRolesExist(body.Roles); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(body.Password), bcrypt.DefaultCost)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not hash the password"})
		return
	}
	err = s.store.CreateLocalUser(body.Username, body.Email, string(hash), store.NormaliseRoles(body.Roles))
	s.logAccessChange(r, "user-create", body.Username, err)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"status": "created"})
}

type inviteRequest struct {
	Username string   `json:"username"`
	Email    string   `json:"email"`
	Roles    []string `json:"roles"`
}

// handleInvite grants roles to somebody who has not signed in yet.
//
// Works under any provider, unlike creating an account: there is no password
// here and nothing to sign in with. It is a standing grant attached to a name,
// which becomes an ordinary account the first time somebody authenticates under
// it.
//
// The name is not checked against anything. Under OIDC there is nothing to
// check it against short of the provider's admin API, and a wrong name fails
// silently — it simply never matches. That is why an unclaimed invitation is
// marked as such in the list rather than blending in: noticing one that has sat
// there for a fortnight is the only way to catch a typo.
func (s *Server) handleInvite(w http.ResponseWriter, r *http.Request) {
	var body inviteRequest
	if err := decodeJSON(w, r, &body); err != nil {
		return
	}
	if err := store.ValidateUsername(body.Username); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	roles := store.NormaliseRoles(body.Roles)
	if len(roles) == 0 {
		// An invitation with no roles grants nothing and would silently do
		// nothing when claimed. Whoever meant to prepare access should be told
		// they have not.
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "an invitation with no roles would grant nothing when it is claimed",
		})
		return
	}
	if err := s.checkRolesExist(roles); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	err := s.store.Invite(body.Username, body.Email, roles)
	// Recorded as the grant it is: the access exists from this moment, waiting
	// for somebody to arrive and take it.
	s.logAccessChange(r, "user-invite", body.Username+" -> "+joinOrNone(roles), err)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"status": "invited"})
}

type setPasswordRequest struct {
	Password string `json:"password"`
}

// handleSetPassword changes the password of a locally created account.
func (s *Server) handleSetPassword(w http.ResponseWriter, r *http.Request) {
	username := chi.URLParam(r, "username")
	var body setPasswordRequest
	if err := decodeJSON(w, r, &body); err != nil {
		return
	}
	if len(body.Password) < minPasswordLength {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "the password is too short"})
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(body.Password), bcrypt.DefaultCost)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not hash the password"})
		return
	}
	err = s.store.SetPassword(username, string(hash))
	// The password itself is never recorded, only that it was changed and by whom.
	s.logAccessChange(r, "user-password", username, err)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

type setRolesRequest struct {
	Roles []string `json:"roles"`
}

// handleSetRoles replaces a user's roles. Takes effect at once: roles are read
// on every request rather than carried in the session.
func (s *Server) handleSetRoles(w http.ResponseWriter, r *http.Request) {
	username := chi.URLParam(r, "username")
	var body setRolesRequest
	if err := decodeJSON(w, r, &body); err != nil {
		return
	}
	roles := store.NormaliseRoles(body.Roles)
	if err := s.checkRolesExist(roles); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	err := s.store.SetRoles(username, roles)
	s.logAccessChange(r, "user-roles", username+" -> "+joinOrNone(roles), err)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

// handleDeleteUser removes a locally created account.
func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	username := chi.URLParam(r, "username")
	actor, _ := auth.FromContext(r.Context())
	if actor != nil && equalFold(actor.Username, username) {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "you cannot delete the account you are signed in with",
		})
		return
	}
	err := s.store.DeleteUser(username)
	s.logAccessChange(r, "user-delete", username, err)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// --- roles -----------------------------------------------------------------

// handleListRoles returns the roles plus the operations they may grant. The
// operations are the deployment's vocabulary — the names the code checks — so
// the editor offers exactly those and nothing invented.
func (s *Server) handleListRoles(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"roles":      s.store.ListRoles(),
		"operations": s.operations,
		// How many people hold each role. Shown in the list so the question
		// "who has this" has an answer without scanning the users table, while
		// the role editor stays about what a role permits.
		"holders": s.store.RoleHolders(),
	})
}

// handleConfigPaths serves the role editor: what paths exist, and what they are
// written relative to.
//
// Kept off /roles deliberately. Listing means walking the whole repository or
// key/value store, and /roles is loaded by both tabs of Access management every
// time either is opened — which put a network round trip in front of a page
// that has nothing to do with configuration. Here it is paid once, when the
// editor actually opens.
func (s *Server) handleConfigPaths(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{
		// Paths in a grant are relative to this. Saying so in the editor is the
		// difference between a working grant and one that silently matches
		// nothing, which looks exactly like a broken portal.
		"root": s.configs.Root(),
	}
	paths, err := s.configPathSuggestions(r.Context())
	if err != nil {
		// Not fatal. The field takes free text, so the editor still works;
		// only the suggestions are missing.
		out["error"] = err.Error()
	} else {
		out["paths"] = paths
	}
	writeJSON(w, http.StatusOK, out)
}

// configPathSuggestions lists the folders under the root, and the entries
// themselves. Folders come first: a grant is normally given on a folder, and a
// grant on one file is the exception.
//
// Deliberately not filtered by the grants of the person looking: someone
// assigning access has to be able to name a path they themselves cannot read.
func (s *Server) configPathSuggestions(ctx context.Context) ([]string, error) {
	entries, err := s.configs.List(ctx)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var folders, files []string
	for _, e := range entries {
		parts := strings.Split(e.Path, "/")
		for i := 1; i < len(parts); i++ {
			p := strings.Join(parts[:i], "/")
			if !seen[p] {
				seen[p] = true
				folders = append(folders, p)
			}
		}
		files = append(files, e.Path)
	}
	sort.Strings(folders)
	return append(folders, files...), nil
}

type saveRoleRequest struct {
	Description string                  `json:"description"`
	Namespaces  []config.NamespaceGrant `json:"namespaces"`
	Global      []string                `json:"global"`
	Configs     []config.ConfigGrant    `json:"configs"`
}

// handleSaveRole creates or replaces a role, then swaps the new model in so the
// change applies to requests already in flight.
func (s *Server) handleSaveRole(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	var body saveRoleRequest
	if err := decodeJSON(w, r, &body); err != nil {
		return
	}
	spec := config.RoleSpec{
		Description: body.Description,
		Namespaces:  body.Namespaces,
		Global:      body.Global,
		Configs:     body.Configs,
	}
	// Validate before saving: a role naming an operation the code does not know
	// grants nothing, and would look like a permissions bug later.
	model := &config.RBACConfig{Operations: s.operations, Roles: map[string]config.RoleSpec{name: spec}}
	if err := model.Validate(); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	err := s.store.SaveRole(name, spec)
	s.logAccessChange(r, "role-save", name, err)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	s.authz.Replace(s.store.AccessModel(s.operations))
	writeJSON(w, http.StatusOK, map[string]string{"status": "saved"})
}

type membershipRequest struct {
	Users []string `json:"users"`
	// Add is false to take the role away. One role either way, never a
	// replacement of anybody's whole set — see Store.ChangeRoleMembership.
	Add bool `json:"add"`
}

// handleRoleMembership grants or revokes one role across several people at once.
//
// The endpoint is named after the role because that is what changes, but the
// screen it serves is the list of users: deciding who gets what and deciding
// what a role may do are different jobs, and mixing them into one editor is how
// the wrong one gets changed.
func (s *Server) handleRoleMembership(w http.ResponseWriter, r *http.Request) {
	role := chi.URLParam(r, "name")
	var body membershipRequest
	if err := decodeJSON(w, r, &body); err != nil {
		return
	}
	if len(body.Users) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "no users given"})
		return
	}

	res, err := s.store.ChangeRoleMembership(role, body.Users, body.Add)
	if err != nil {
		writeStoreError(w, err)
		return
	}

	// One entry per person, in the same shape as a single-user change: a review
	// starts from "what happened to this user", and a bulk action must not fall
	// out of a search by name.
	verb := "role-granted"
	if !body.Add {
		verb = "role-revoked"
	}
	for _, name := range res.Changed {
		s.logAccessChange(r, verb, name+" -> "+role, nil)
	}
	s.authz.Replace(s.store.AccessModel(s.operations))

	// Taking away a role the deployment itself grants does nothing: bootstrap
	// roles are added on top of the stored ones at request time. The store did
	// change, so this is not an error — but reporting it as done would be a
	// lie, and the person would stay an administrator.
	stillGranted := []string{}
	if !body.Add {
		for _, name := range res.Changed {
			if s.cfg.Auth.Bootstrap.Grants(name) && contains(s.cfg.Auth.Bootstrap.Roles, role) {
				stillGranted = append(stillGranted, name)
			}
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"changed":      nonNil(res.Changed),
		"unchanged":    nonNil(res.Unchanged),
		"unknown":      nonNil(res.Unknown),
		"stillGranted": stillGranted,
	})
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// handleDeleteRole removes a role and reports who was holding it.
func (s *Server) handleDeleteRole(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	affected, err := s.store.DeleteRole(name)
	s.logAccessChange(r, "role-delete", name, err)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	s.authz.Replace(s.store.AccessModel(s.operations))
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "deleted",
		// Named so the UI can say whose access just changed, rather than
		// leaving it to be discovered by whoever lost it.
		"affectedUsers": nonNil(affected),
	})
}

// --- helpers ---------------------------------------------------------------

func (s *Server) checkRolesExist(roles []string) error {
	for _, r := range roles {
		if !s.store.RoleExists(r) {
			return errors.New("no such role: " + r)
		}
	}
	return nil
}

func decodeJSON(w http.ResponseWriter, r *http.Request, v any) error {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(v); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "malformed request body"})
		return err
	}
	return nil
}

func joinOrNone(roles []string) string {
	if len(roles) == 0 {
		return "(no roles)"
	}
	out := roles[0]
	for _, r := range roles[1:] {
		out += ", " + r
	}
	return out
}
