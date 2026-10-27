package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"devops-tools/internal/audit"
	"devops-tools/internal/auth"
	"devops-tools/internal/kube"
)

// maxRequestBody bounds what the page may send: a request with its body, or a
// whole collection on import.
const maxRequestBody = 2 << 20

// handleRequestCollection returns the saved requests of a service, and what
// this person may do with them.
func (s *Server) handleRequestCollection(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.FromContext(r.Context())
	namespace, name := chi.URLParam(r, "namespace"), chi.URLParam(r, "name")

	c, err := s.kube.RequestCollection(r.Context(), user.Username, namespace, name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"collection": c,
		"mayWrite":   s.authz.Allowed(user.Roles, namespace, OpServiceRequestWrite),
		// Whether {{secret:…}} will be filled in when this person sends.
		"maySecrets": s.authz.Allowed(user.Roles, namespace, OpSecretRead),
	})
}

// handleSaveRequests adds, replaces and removes saved requests in one write.
func (s *Server) handleSaveRequests(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.FromContext(r.Context())
	namespace, name := chi.URLParam(r, "namespace"), chi.URLParam(r, "name")

	var body struct {
		Version string              `json:"version"`
		Save    []kube.SavedRequest `json:"save"`
		Remove  []string            `json:"remove"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequestBody)).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	c, detail, err := s.kube.SaveRequests(r.Context(), user.Username, namespace, name, body.Version, body.Save, body.Remove)
	s.audit.Log(audit.Entry{
		User: user.Username, Environment: s.cfg.Environment, Namespace: namespace,
		Operation: OpServiceRequestWrite, Target: name, Allowed: true,
		Success: err == nil, Error: errStr(err), Detail: detail,
	})
	switch {
	case errors.Is(err, kube.ErrCollectionChanged):
		http.Error(w, err.Error(), http.StatusConflict)
	case errors.Is(err, kube.ErrCollectionReadOnly), errors.Is(err, kube.ErrCollectionFull):
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
	case apierrors.IsForbidden(err):
		http.Error(w, err.Error(), http.StatusForbidden)
	case errors.Is(err, kube.ErrInvalidRequest):
		http.Error(w, err.Error(), http.StatusBadRequest)
	case err != nil:
		http.Error(w, err.Error(), http.StatusBadGateway)
	default:
		writeJSON(w, http.StatusOK, c)
	}
}

// handleSendRequest sends one request to the service and returns the answer.
//
// The trail records the method, the port, the path and the status — never the
// query string, the headers or either body: those are where tokens and
// personal data travel.
func (s *Server) handleSendRequest(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.FromContext(r.Context())
	namespace, name := chi.URLParam(r, "namespace"), chi.URLParam(r, "name")

	var req kube.OutgoingRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequestBody)).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	req.Method = strings.ToUpper(strings.TrimSpace(req.Method))
	if !kube.ReadOnlyMethod(req.Method) && !s.authz.Allowed(user.Roles, namespace, OpServiceRequestWrite) {
		s.audit.Log(audit.Entry{
			User: user.Username, Environment: s.cfg.Environment, Namespace: namespace,
			Operation: OpServiceRequestWrite, Target: name, Allowed: false,
		})
		http.Error(w, "sending "+req.Method+" needs "+OpServiceRequestWrite, http.StatusForbidden)
		return
	}

	// A secret is filled in only for someone who may read it — the same rule
	// as opening it on the Secrets page.
	secrets := func(secret, key string) (string, error) {
		if !s.authz.Allowed(user.Roles, namespace, OpSecretRead) {
			return "", errors.New("using a secret needs " + OpSecretRead)
		}
		sd, err := s.kube.GetSecret(r.Context(), user.Username, namespace, secret)
		if err != nil {
			return "", err
		}
		v, ok := sd.Data[key]
		if !ok {
			return "", errors.New("the secret has no such key")
		}
		return v, nil
	}

	res, err := s.kube.SendRequest(r.Context(), user.Username, namespace, name, req, secrets)

	op := OpServiceRequest
	if !kube.ReadOnlyMethod(req.Method) {
		op = OpServiceRequestWrite
	}
	entry := audit.Entry{
		User: user.Username, Environment: s.cfg.Environment, Namespace: namespace,
		Operation: op, Target: name, Allowed: true,
		Success: err == nil && res.Status > 0, Error: errStr(err),
	}
	if err == nil {
		outcome := res.Error
		if res.Status > 0 {
			outcome = strconv.Itoa(res.Status)
		}
		path, _, _ := strings.Cut(req.Path, "?")
		entry.Detail = fmt.Sprintf("%s :%s%s → %s in %d ms", req.Method, portOf(res.URL), path, outcome, res.Millis)
		if res.Status == 0 {
			entry.Error = res.Error
		}
	}
	s.audit.Log(entry)

	switch {
	case apierrors.IsNotFound(err):
		http.Error(w, "service not found", http.StatusNotFound)
	case err != nil:
		http.Error(w, err.Error(), http.StatusBadRequest)
	default:
		writeJSON(w, http.StatusOK, res)
	}
}

// portOf is the port of a URL, for the trail.
func portOf(u string) string {
	i := strings.LastIndex(u, ":")
	if i < 0 {
		return ""
	}
	rest := u[i+1:]
	if j := strings.IndexByte(rest, '/'); j >= 0 {
		rest = rest[:j]
	}
	return rest
}

// handleServiceAPI lists the operations a service describes in its OpenAPI
// document, for the page to offer as requests.
func (s *Server) handleServiceAPI(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.FromContext(r.Context())
	namespace, name := chi.URLParam(r, "namespace"), chi.URLParam(r, "name")
	port, _ := strconv.Atoi(r.URL.Query().Get("port"))

	source, eps, err := s.kube.ServiceAPI(r.Context(), user.Username, namespace, name, int32(port))
	switch {
	case errors.Is(err, kube.ErrNoAPIDocs):
		writeJSON(w, http.StatusOK, map[string]any{"endpoints": []kube.APIEndpoint{}})
	case apierrors.IsNotFound(err):
		http.Error(w, "service not found", http.StatusNotFound)
	case err != nil:
		http.Error(w, err.Error(), http.StatusBadRequest)
	default:
		writeJSON(w, http.StatusOK, map[string]any{"source": source, "endpoints": eps})
	}
}
