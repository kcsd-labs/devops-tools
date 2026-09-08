package api

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	"devops-tools/internal/audit"
	"devops-tools/internal/auth"
)

func (s *Server) handleListSecrets(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.FromContext(r.Context())
	namespace := chi.URLParam(r, "namespace")
	includeSystem := r.URL.Query().Get("system") == "true"

	secrets, err := s.kube.ListSecrets(r.Context(), user.Username, namespace, includeSystem)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, secrets)
}

// handleGetSecret returns the decoded contents of a secret. Reading a secret is
// audited just like writing one: "who looked at this" is a question security
// reviews ask, and the list endpoint alone cannot answer it.
func (s *Server) handleGetSecret(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.FromContext(r.Context())
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	sec, err := s.kube.GetSecret(r.Context(), user.Username, namespace, name)
	s.audit.Log(audit.Entry{
		User: user.Username, Environment: s.cfg.Environment, Namespace: namespace,
		Operation: OpSecretRead, Target: name, Allowed: true,
		Success: err == nil, Error: errStr(err),
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, sec)
}

type secretBody struct {
	Name string            `json:"name"`
	Type string            `json:"type"`
	Data map[string]string `json:"data"`
}

func (s *Server) handleCreateSecret(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.FromContext(r.Context())
	namespace := chi.URLParam(r, "namespace")

	var body secretBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Name == "" {
		http.Error(w, "bad request: name is required", http.StatusBadRequest)
		return
	}

	err := s.kube.CreateSecret(r.Context(), user.Username, namespace, body.Name, body.Type, body.Data)
	s.audit.Log(audit.Entry{
		User: user.Username, Environment: s.cfg.Environment, Namespace: namespace,
		Operation: OpSecretCreate, Target: body.Name, Allowed: true,
		Success: err == nil, Error: errStr(err),
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "created", "secret": body.Name})
}

func (s *Server) handleUpdateSecret(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.FromContext(r.Context())
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	var body secretBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	err := s.kube.UpdateSecret(r.Context(), user.Username, namespace, name, body.Data)
	s.audit.Log(audit.Entry{
		User: user.Username, Environment: s.cfg.Environment, Namespace: namespace,
		Operation: OpSecretUpdate, Target: name, Allowed: true,
		Success: err == nil, Error: errStr(err),
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "updated", "secret": name})
}

func (s *Server) handleDeleteSecret(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.FromContext(r.Context())
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	err := s.kube.DeleteSecret(r.Context(), user.Username, namespace, name)
	s.audit.Log(audit.Entry{
		User: user.Username, Environment: s.cfg.Environment, Namespace: namespace,
		Operation: OpSecretDelete, Target: name, Allowed: true,
		Success: err == nil, Error: errStr(err),
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted", "secret": name})
}
