package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"devops-tools/internal/audit"
	"devops-tools/internal/auth"
	"devops-tools/internal/kube"
)

// handleIngressNamespaces lists the namespaces whose ingresses this person may
// see, as the Services page lists its own.
func (s *Server) handleIngressNamespaces(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.FromContext(r.Context())
	namespaces := s.authz.NamespacesForOp(user.Roles, OpIngressList)
	if len(namespaces) == 1 && namespaces[0] == "*" {
		all, err := s.kube.ListNamespaces(r.Context(), user.Username, s.cfg.Cluster.HiddenNamespaces)
		if err != nil {
			http.Error(w, "could not list the namespaces: "+err.Error(), http.StatusBadGateway)
			return
		}
		namespaces = unionSorted(all, s.authz.NamedNamespaces(user.Roles, OpIngressList))
	}
	if len(namespaces) == 0 {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"namespaces": namespaces})
}

// handleListIngresses lists every route the namespace's ingresses answer, each
// with what stands in its way, and what this person may do about it.
func (s *Server) handleListIngresses(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.FromContext(r.Context())
	namespace := chi.URLParam(r, "namespace")
	list, err := s.kube.ListIngresses(r.Context(), user.Username, namespace)
	if err != nil {
		slog.Warn("list ingresses", "namespace", namespace, "user", user.Username, "err", err)
		http.Error(w, err.Error(), kubeStatus(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"routes":         list.Routes,
		"annotationKeys": list.AnnotationKeys,
		"classes":        list.Classes,
		"certificates":   list.Certificates,
		"mayCreate":      s.authz.Allowed(user.Roles, namespace, OpIngressCreate),
		"mayUpdate":      s.authz.Allowed(user.Roles, namespace, OpIngressUpdate),
		"mayDelete":      s.authz.Allowed(user.Roles, namespace, OpIngressDelete),
	})
}

func (s *Server) handleGetIngress(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.FromContext(r.Context())
	d, err := s.kube.GetIngress(r.Context(), user.Username, chi.URLParam(r, "namespace"), chi.URLParam(r, "name"))
	if err != nil {
		http.Error(w, err.Error(), kubeStatus(err))
		return
	}
	writeJSON(w, http.StatusOK, d)
}

// handleSuggestIngress fills in a new ingress for a service from what the
// cluster already does.
func (s *Server) handleSuggestIngress(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.FromContext(r.Context())
	sg, err := s.kube.SuggestIngress(r.Context(), user.Username, chi.URLParam(r, "namespace"), r.URL.Query().Get("service"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	writeJSON(w, http.StatusOK, sg)
}

type ingressBody struct {
	Name    string            `json:"name"`
	Version string            `json:"version"`
	Form    *kube.IngressForm `json:"form"`
	YAML    *string           `json:"yaml"`
}

func decodeIngress(w http.ResponseWriter, r *http.Request) (ingressBody, bool) {
	var b ingressBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil || (b.Form == nil && b.YAML == nil) {
		http.Error(w, "bad request: form or yaml is required", http.StatusBadRequest)
		return b, false
	}
	return b, true
}

// ingressStatus maps a failed save to its status: the cluster's own errors as
// they are, the form's and the YAML's as a request to fix them.
func ingressStatus(err error) int {
	if errors.Is(err, kube.ErrIngressChanged) {
		return http.StatusConflict
	}
	var st apierrors.APIStatus
	if errors.As(err, &st) {
		return kubeStatus(err)
	}
	return http.StatusBadRequest
}

// handleCheckIngress runs the list's checks on an ingress before it is saved.
func (s *Server) handleCheckIngress(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.FromContext(r.Context())
	b, ok := decodeIngress(w, r)
	if !ok || b.Form == nil {
		return
	}
	routes, err := s.kube.CheckIngress(r.Context(), user.Username, chi.URLParam(r, "namespace"), b.Name, *b.Form)
	if err != nil {
		http.Error(w, err.Error(), ingressStatus(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"routes": routes})
}

func (s *Server) handleCreateIngress(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.FromContext(r.Context())
	namespace := chi.URLParam(r, "namespace")
	b, ok := decodeIngress(w, r)
	if !ok {
		return
	}
	if b.Name == "" {
		http.Error(w, "bad request: name is required", http.StatusBadRequest)
		return
	}
	detail, err := s.kube.CreateIngress(r.Context(), user.Username, namespace, b.Name,
		kube.IngressChange{Form: b.Form, YAML: b.YAML})
	s.audit.Log(audit.Entry{
		User: user.Username, Environment: s.cfg.Environment, Namespace: namespace,
		Operation: OpIngressCreate, Target: b.Name, Allowed: true,
		Success: err == nil, Error: errStr(err), Detail: detail,
	})
	if err != nil {
		http.Error(w, err.Error(), ingressStatus(err))
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"status": "created", "detail": detail})
}

func (s *Server) handleUpdateIngress(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.FromContext(r.Context())
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")
	b, ok := decodeIngress(w, r)
	if !ok {
		return
	}
	doc, detail, err := s.kube.UpdateIngress(r.Context(), user.Username, namespace, name, b.Version,
		kube.IngressChange{Form: b.Form, YAML: b.YAML})
	s.audit.Log(audit.Entry{
		User: user.Username, Environment: s.cfg.Environment, Namespace: namespace,
		Operation: OpIngressUpdate, Target: name, Allowed: true,
		Success: err == nil, Error: errStr(err), Detail: detail,
	})
	if err != nil {
		http.Error(w, err.Error(), ingressStatus(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ingress": doc, "detail": detail})
}

func (s *Server) handleDeleteIngress(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.FromContext(r.Context())
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")
	detail, err := s.kube.DeleteIngress(r.Context(), user.Username, namespace, name)
	s.audit.Log(audit.Entry{
		User: user.Username, Environment: s.cfg.Environment, Namespace: namespace,
		Operation: OpIngressDelete, Target: name, Allowed: true,
		Success: err == nil, Error: errStr(err), Detail: detail,
	})
	if err != nil {
		http.Error(w, err.Error(), kubeStatus(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}
