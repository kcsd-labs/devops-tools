package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"devops-tools/internal/audit"
	"devops-tools/internal/auth"
	"devops-tools/internal/kube"
)

// A ConfigMap holds at most 1 MiB; a little more is allowed for the JSON
// around it, and Kubernetes has the last word.
const maxConfigMapBody = 1<<20 + 64<<10

// handleListConfigMaps lists the ConfigMaps in a namespace, and what this
// person may do with them, so the page offers only what will work.
func (s *Server) handleListConfigMaps(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.FromContext(r.Context())
	namespace := chi.URLParam(r, "namespace")

	list, err := s.kube.ListConfigMaps(r.Context(), user.Username, namespace)
	if err != nil {
		http.Error(w, err.Error(), kubeStatus(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"configMaps": list,
		"mayCreate":  s.authz.Allowed(user.Roles, namespace, OpConfigMapCreate),
		"mayUpdate":  s.authz.Allowed(user.Roles, namespace, OpConfigMapUpdate),
		"mayDelete":  s.authz.Allowed(user.Roles, namespace, OpConfigMapDelete),
		"mayRestart": s.authz.Allowed(user.Roles, namespace, OpPodRestart),
	})
}

func (s *Server) handleGetConfigMap(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.FromContext(r.Context())
	namespace, name := chi.URLParam(r, "namespace"), chi.URLParam(r, "name")

	cm, err := s.kube.GetConfigMap(r.Context(), user.Username, namespace, name)
	if err != nil {
		http.Error(w, err.Error(), kubeStatus(err))
		return
	}
	writeJSON(w, http.StatusOK, cm)
}

// handleExportConfigMap returns the ConfigMap as a manifest to take to another
// environment.
func (s *Server) handleExportConfigMap(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.FromContext(r.Context())
	namespace, name := chi.URLParam(r, "namespace"), chi.URLParam(r, "name")

	b, err := s.kube.ExportConfigMap(r.Context(), user.Username, namespace, name)
	if err != nil {
		http.Error(w, err.Error(), kubeStatus(err))
		return
	}
	w.Header().Set("Content-Type", "application/yaml")
	_, _ = w.Write(b)
}

// handleCreateConfigMap creates a ConfigMap, either from a name and values or
// from a manifest — the import.
func (s *Server) handleCreateConfigMap(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.FromContext(r.Context())
	namespace := chi.URLParam(r, "namespace")

	var body struct {
		Name     string            `json:"name"`
		Data     map[string]string `json:"data"`
		YAML     string            `json:"yaml"`
		Manifest string            `json:"manifest"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxConfigMapBody)).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if body.Manifest != "" {
		name, data, err := kube.ParseConfigMapManifest([]byte(body.Manifest))
		if err != nil {
			http.Error(w, "cannot read the manifest: "+err.Error(), http.StatusBadRequest)
			return
		}
		body.Name, body.Data = name, data
	}
	if body.YAML != "" {
		data, err := kube.ParseConfigMapData(body.YAML)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		body.Data = data
	}
	if body.Data == nil {
		body.Data = map[string]string{}
	}

	detail, err := s.kube.CreateConfigMap(r.Context(), user.Username, namespace, body.Name, body.Data)
	s.audit.Log(audit.Entry{
		User: user.Username, Environment: s.cfg.Environment, Namespace: namespace,
		Operation: OpConfigMapCreate, Target: body.Name, Allowed: true,
		Success: err == nil, Error: errStr(err), Detail: detail,
	})
	if err != nil {
		http.Error(w, err.Error(), kubeStatus(err))
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"name": body.Name})
}

// handleUpdateConfigMap replaces the text values of a ConfigMap. The version
// read with it must still be current: the page holds a copy for as long as
// someone is editing, and somebody else may have saved in the meantime.
func (s *Server) handleUpdateConfigMap(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.FromContext(r.Context())
	namespace, name := chi.URLParam(r, "namespace"), chi.URLParam(r, "name")

	// Either the values, or the data: block as text — the page's default view
	// — which is read here so that the page and kubectl read it alike.
	var body struct {
		Version string            `json:"version"`
		Data    map[string]string `json:"data"`
		YAML    *string           `json:"yaml"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxConfigMapBody)).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if body.YAML != nil {
		data, err := kube.ParseConfigMapData(*body.YAML)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		body.Data = data
	}
	if body.Data == nil {
		body.Data = map[string]string{}
	}

	cm, detail, err := s.kube.UpdateConfigMap(r.Context(), user.Username, namespace, name, body.Version, body.Data)
	s.audit.Log(audit.Entry{
		User: user.Username, Environment: s.cfg.Environment, Namespace: namespace,
		Operation: OpConfigMapUpdate, Target: name, Allowed: true,
		Success: err == nil, Error: errStr(err), Detail: detail,
	})
	switch {
	case errors.Is(err, kube.ErrConfigMapChanged):
		http.Error(w, err.Error(), http.StatusConflict)
	case errors.Is(err, kube.ErrConfigMapImmutable):
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
	case err != nil:
		http.Error(w, err.Error(), kubeStatus(err))
	default:
		writeJSON(w, http.StatusOK, map[string]any{"configMap": cm, "detail": detail})
	}
}

func (s *Server) handleDeleteConfigMap(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.FromContext(r.Context())
	namespace, name := chi.URLParam(r, "namespace"), chi.URLParam(r, "name")

	detail, err := s.kube.DeleteConfigMap(r.Context(), user.Username, namespace, name)
	s.audit.Log(audit.Entry{
		User: user.Username, Environment: s.cfg.Environment, Namespace: namespace,
		Operation: OpConfigMapDelete, Target: name, Allowed: true,
		Success: err == nil, Error: errStr(err), Detail: detail,
	})
	if err != nil {
		http.Error(w, err.Error(), kubeStatus(err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleRestartWorkload rolls a workload out, named directly.
func (s *Server) handleRestartWorkload(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.FromContext(r.Context())
	namespace := chi.URLParam(r, "namespace")
	wl := kube.WorkloadRef{Kind: chi.URLParam(r, "kind"), Name: chi.URLParam(r, "name")}

	_, err := s.kube.RestartWorkload(r.Context(), user.Username, namespace, wl)
	s.audit.Log(audit.Entry{
		User: user.Username, Environment: s.cfg.Environment, Namespace: namespace,
		Operation: OpPodRestart, Target: wl.Kind + "/" + wl.Name, Allowed: true,
		Success: err == nil, Error: errStr(err),
	})
	switch {
	case errors.Is(err, kube.ErrNoController):
		http.Error(w, "only a Deployment, StatefulSet or DaemonSet can be restarted", http.StatusBadRequest)
	case err != nil:
		http.Error(w, err.Error(), kubeStatus(err))
	default:
		writeJSON(w, http.StatusOK, map[string]string{"status": "restarted", "kind": wl.Kind, "name": wl.Name})
	}
}

// kubeStatus turns an error from the Kubernetes API into the status to answer
// with: what the API said, where it said something a person can act on.
func kubeStatus(err error) int {
	switch {
	case errors.Is(err, kube.ErrConfigMapSystem), errors.Is(err, kube.ErrSecretSystem):
		return http.StatusUnprocessableEntity
	case apierrors.IsNotFound(err):
		return http.StatusNotFound
	case apierrors.IsAlreadyExists(err), apierrors.IsConflict(err):
		return http.StatusConflict
	case apierrors.IsInvalid(err), apierrors.IsBadRequest(err):
		return http.StatusBadRequest
	case apierrors.IsForbidden(err):
		return http.StatusForbidden
	}
	return http.StatusBadGateway
}
