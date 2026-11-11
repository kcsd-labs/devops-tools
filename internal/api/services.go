package api

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"devops-tools/internal/auth"
	"devops-tools/internal/kube"
)

// handleServiceNamespaces lists the namespaces the Services page may offer:
// the ones the caller holds service-list in, or every namespace there is for a
// wildcard grant, less the ones the deployment hides.
//
// Only the names. The page reads one namespace at a time, chosen by the
// person looking: with dozens of namespaces, reading them all for every visit
// cost seconds and produced a list nobody could take in.
func (s *Server) handleServiceNamespaces(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.FromContext(r.Context())
	namespaces := s.authz.NamespacesForOp(user.Roles, OpServiceList)
	if len(namespaces) == 1 && namespaces[0] == "*" {
		all, err := s.kube.ListNamespaces(r.Context(), user.Username, s.cfg.Cluster.HiddenNamespaces)
		if err != nil {
			http.Error(w, "could not list the namespaces: "+err.Error(), http.StatusBadGateway)
			return
		}
		namespaces = unionSorted(all, s.authz.NamedNamespaces(user.Roles, OpServiceList))
	}
	if len(namespaces) == 0 {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"namespaces": namespaces})
}

// handleServices lists where the services in one namespace answer.
func (s *Server) handleServices(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.FromContext(r.Context())
	namespace := chi.URLParam(r, "namespace")

	svcs, err := s.kube.ListServices(r.Context(), user.Username, namespace)
	if err != nil {
		slog.Warn("list services", "namespace", namespace, "user", user.Username, "err", err)
		http.Error(w, "could not read the services in "+namespace+": "+err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"services": svcs,
		// The suffix of a fully qualified name on this cluster.
		"clusterDomain": kube.ClusterDomain(),
		// Which of the row's actions lead somewhere this person may go. A
		// button that opens onto "forbidden" is worse than no button.
		"mayLogs":    s.authz.Allowed(user.Roles, namespace, OpLogs),
		"mayHelm":    s.authz.Allowed(user.Roles, namespace, OpHelmList),
		"mayRequest": s.authz.Allowed(user.Roles, namespace, OpServiceRequest),
		"mayIngress": s.authz.Allowed(user.Roles, namespace, OpIngressList),
	})
}
