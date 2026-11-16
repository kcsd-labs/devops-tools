package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"devops-tools/internal/audit"
	"devops-tools/internal/auth"
	"devops-tools/internal/config"
	"devops-tools/internal/helmreq"
)

// A new namespace, and the starting values administrators keep per chart.
//
// Roles name their namespaces one by one, or "*". A namespace created for a
// developer's request is therefore one their role does not reach — they
// would lose sight of their own release the moment it was installed. So the
// administrator approving it can give the requester's roles the namespace,
// with the same operations each already holds in the namespaces it names.

// grantOps are the operations a role holds in the namespaces it names — what
// it is given in a new one.
func grantOps(spec config.RoleSpec) []string {
	seen := map[string]bool{}
	for _, g := range spec.Namespaces {
		if g.Namespace == "*" {
			continue
		}
		for _, op := range g.Operations {
			seen[op] = true
		}
	}
	out := make([]string, 0, len(seen))
	for op := range seen {
		out = append(out, op)
	}
	sort.Strings(out)
	return out
}

func (s *Server) roleSpec(name string) (config.RoleSpec, bool) {
	for _, r := range s.store.ListRoles() {
		if r.Name == name {
			return r.RoleSpec, true
		}
	}
	return config.RoleSpec{}, false
}

// grantNamespace gives a role ns, with the operations it holds elsewhere.
func (s *Server) grantNamespace(r *http.Request, role, ns string) error {
	spec, ok := s.roleSpec(role)
	if !ok {
		return fmt.Errorf("there is no role %s", role)
	}
	ops := grantOps(spec)
	if len(ops) == 0 {
		return fmt.Errorf("%s names no namespace to copy operations from", role)
	}
	for _, g := range spec.Namespaces {
		if g.Namespace == ns {
			return nil
		}
	}
	spec.Namespaces = append(append([]config.NamespaceGrant{}, spec.Namespaces...),
		config.NamespaceGrant{Namespace: ns, Operations: ops})
	err := s.store.SaveRole(role, spec)
	s.logAccessChange(r, "role-save", role, err)
	if err != nil {
		return err
	}
	s.authz.Replace(s.store.AccessModel(s.operations))
	return nil
}

// roleGrant is one of a requester's roles, as the approval offers it.
type roleGrant struct {
	Role       string   `json:"role"`
	Operations []string `json:"operations"`
	// Covered: the role reaches the namespace already, through "*".
	Covered bool `json:"covered"`
}

// handleRequestGrants lists what giving the new namespace of a request to
// each of the requester's roles would grant.
func (s *Server) handleRequestGrants(w http.ResponseWriter, r *http.Request) {
	q, err := s.catalog.requests.Get(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		requestError(w, err)
		return
	}
	out := []roleGrant{}
	if !q.NewNamespace {
		writeJSON(w, http.StatusOK, out)
		return
	}
	for _, role := range s.store.RolesFor(q.Requester) {
		spec, ok := s.roleSpec(role)
		if !ok {
			continue
		}
		covered := s.authz.Allowed([]string{role}, q.Namespace, OpHelmUpgrade) ||
			s.authz.Allowed([]string{role}, q.Namespace, OpHelmList)
		out = append(out, roleGrant{Role: role, Operations: grantOps(spec), Covered: covered})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleSaveTemplate(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.FromContext(r.Context())
	repo, chart := chi.URLParam(r, "repo"), chi.URLParam(r, "chart")
	var body struct {
		Values string `json:"values"`
		Note   string `json:"note"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 512<<10)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "the request does not read: " + err.Error()})
		return
	}
	vals, err := parseValues(body.Values)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if strings.TrimSpace(body.Values) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "there are no values to start from — remove the starting values instead"})
		return
	}
	t := &helmreq.Template{Repo: repo, Chart: chart, Values: body.Values, Note: strings.TrimSpace(body.Note),
		Author: user.Username, Updated: time.Now().UTC()}
	err = s.catalog.templates.Put(r.Context(), t)
	s.audit.Log(audit.Entry{User: user.Username, Environment: s.cfg.Environment, Namespace: "-",
		Operation: "helm-template-save", Target: repo + "/" + chart, Allowed: true, Success: err == nil,
		Error: errStr(err), Detail: "values: " + strings.Join(valueKeys(vals), ", ")})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *Server) handleDeleteTemplate(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.FromContext(r.Context())
	repo, chart := chi.URLParam(r, "repo"), chi.URLParam(r, "chart")
	err := s.catalog.templates.Delete(r.Context(), repo, chart)
	s.audit.Log(audit.Entry{User: user.Username, Environment: s.cfg.Environment, Namespace: "-",
		Operation: "helm-template-delete", Target: repo + "/" + chart, Allowed: true, Success: err == nil,
		Error: errStr(err)})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "removed"})
}
