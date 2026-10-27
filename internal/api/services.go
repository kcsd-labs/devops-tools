package api

import (
	"log/slog"
	"net/http"
	"sort"
	"sync"

	"devops-tools/internal/auth"
	"devops-tools/internal/kube"
)

// servicesConcurrency bounds how many namespaces are read at once. A wildcard
// grant can mean every namespace in the cluster, and two calls each.
const servicesConcurrency = 8

// servicesNamespaces works out which namespaces this caller may list services
// in: the ones they hold service-list in, or every namespace there is for a
// wildcard grant, less the ones the deployment hides.
func (s *Server) servicesNamespaces(r *http.Request, user *auth.User) ([]string, error) {
	granted := s.authz.NamespacesForOp(user.Roles, OpServiceList)
	if len(granted) == 1 && granted[0] == "*" {
		return s.kube.ListNamespaces(r.Context(), user.Username, s.cfg.Cluster.HiddenNamespaces)
	}
	return granted, nil
}

// handleServices lists where services answer, across the namespaces the caller
// holds service-list in.
//
// Gated here rather than by requireOp, as the audit page is: the grant is per
// namespace and the page spans several. Only those namespaces are read at all —
// what someone cannot see is never fetched, rather than fetched and filtered.
func (s *Server) handleServices(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.FromContext(r.Context())

	namespaces, err := s.servicesNamespaces(r, user)
	if err != nil {
		http.Error(w, "could not list the namespaces to read: "+err.Error(), http.StatusBadGateway)
		return
	}
	if len(namespaces) == 0 {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if want := r.URL.Query().Get("namespace"); want != "" {
		if !s.authz.Allowed(user.Roles, want, OpServiceList) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		namespaces = []string{want}
	}

	results := make([][]kube.Service, len(namespaces))
	failed := make([]bool, len(namespaces))
	sem := make(chan struct{}, servicesConcurrency)
	var wg sync.WaitGroup
	for i, ns := range namespaces {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			svcs, err := s.kube.ListServices(r.Context(), user.Username, ns)
			if err != nil {
				// One namespace that cannot be read — removed since it was
				// granted, or refused — does not empty the page. It is named
				// beside what could be read instead.
				slog.Warn("list services", "namespace", ns, "user", user.Username, "err", err)
				failed[i] = true
				return
			}
			results[i] = svcs
		}()
	}
	wg.Wait()

	out := []kube.Service{}
	unreadable := []string{}
	for i, ns := range namespaces {
		if failed[i] {
			unreadable = append(unreadable, ns)
			continue
		}
		out = append(out, results[i]...)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Namespace != out[j].Namespace {
			return out[i].Namespace < out[j].Namespace
		}
		return out[i].Name < out[j].Name
	})

	writeJSON(w, http.StatusOK, map[string]any{
		"services": out,
		// What was read, so the page can say whose services these are — an
		// incomplete list should read as a scope, not as a fault.
		"namespaces": namespaces,
		"unreadable": unreadable,
		// The suffix of a fully qualified name on this cluster.
		"clusterDomain": kube.ClusterDomain(),
	})
}
