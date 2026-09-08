package api

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"devops-tools/internal/audit"
	"devops-tools/internal/auth"
)

func (s *Server) handleListReleases(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.FromContext(r.Context())
	namespace := chi.URLParam(r, "namespace")

	rels, err := s.kube.ListReleases(user.Username, namespace)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, rels)
}

func (s *Server) handleReleaseHistory(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.FromContext(r.Context())
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	hist, err := s.kube.ReleaseHistory(user.Username, namespace, name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, hist)
}

func (s *Server) handleRollback(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.FromContext(r.Context())
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	revision := 0 // 0 means the previous revision
	if v := r.URL.Query().Get("revision"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			revision = n
		}
	}

	err := s.kube.RollbackRelease(user.Username, namespace, name, revision)
	s.audit.Log(audit.Entry{
		User: user.Username, Environment: s.cfg.Environment, Namespace: namespace,
		Operation: OpHelmRollback, Target: name, Allowed: true,
		Success: err == nil, Error: errStr(err),
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "rolled-back", "release": name, "revision": revision})
}

func (s *Server) handleUninstall(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.FromContext(r.Context())
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	warning, err := s.kube.UninstallRelease(user.Username, namespace, name)
	s.audit.Log(audit.Entry{
		User: user.Username, Environment: s.cfg.Environment, Namespace: namespace,
		Operation: OpHelmUninstall, Target: name, Allowed: true,
		Success: err == nil, Error: errStr(err),
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"status": "uninstalled", "release": name, "warning": warning,
	})
}
