package api

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"devops-tools/internal/auth"
)

// handleMultiPodLogs returns the merged logs of several pods (?pods=a,b&tail=N).
func (s *Server) handleMultiPodLogs(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.FromContext(r.Context())
	namespace := chi.URLParam(r, "namespace")

	var pods []string
	for _, p := range strings.Split(r.URL.Query().Get("pods"), ",") {
		if p = strings.TrimSpace(p); p != "" {
			pods = append(pods, p)
		}
	}
	if len(pods) == 0 {
		http.Error(w, "no pods specified", http.StatusBadRequest)
		return
	}

	tail := int64(500)
	if t := r.URL.Query().Get("tail"); t != "" {
		if n, err := strconv.ParseInt(t, 10, 64); err == nil {
			tail = n
		}
	}

	text, err := s.kube.MultiPodLogs(r.Context(), user.Username, namespace, pods, tail)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte(text))
}

// distinctApps resolves the service label of each pod, dropping the ones that
// cannot be resolved. Used to widen a query from pods to whole services.
func (s *Server) distinctApps(ctx context.Context, asUser, namespace string, pods []string) []string {
	seen := map[string]bool{}
	var apps []string
	for _, p := range pods {
		app, err := s.kube.PodAppLabel(ctx, asUser, namespace, p)
		if err != nil || app == "" || seen[app] {
			continue
		}
		seen[app] = true
		apps = append(apps, app)
	}
	sort.Strings(apps)
	return apps
}

// handleLokiQuery serves historical logs from Loki for an arbitrary time range.
//
// The namespace is taken from the URL — where it has already passed the RBAC
// check — and forced into the LogQL selector, so a client cannot craft a query
// that reaches outside the namespace it is allowed to see.
func (s *Server) handleLokiQuery(w http.ResponseWriter, r *http.Request) {
	if s.loki == nil {
		http.Error(w, "historical logs are disabled: Loki is not configured", http.StatusServiceUnavailable)
		return
	}
	user, _ := auth.FromContext(r.Context())
	namespace := chi.URLParam(r, "namespace")
	q := r.URL.Query()

	// Build the stream selector. Scope options:
	//   app=<label>        — a whole service, including pods that no longer exist
	//   pods=a,b           — several named pods
	//   pod=<name>         — a single pod
	// With scope=service the pods are resolved to their service labels first.
	sel := fmt.Sprintf(`namespace=%q`, namespace)
	multiPods := false

	if app := q.Get("app"); app != "" {
		sel += fmt.Sprintf(`,app=%q`, app)
		multiPods = true
	} else if pods := q.Get("pods"); pods != "" {
		var list []string
		for _, p := range strings.Split(pods, ",") {
			if p = strings.TrimSpace(p); p != "" {
				list = append(list, p)
			}
		}
		switch {
		case q.Get("scope") == "service":
			apps := s.distinctApps(r.Context(), user.Username, namespace, list)
			if len(apps) == 0 {
				http.Error(w, "could not resolve the service label of the given pods", http.StatusBadRequest)
				return
			}
			if len(apps) == 1 {
				sel += fmt.Sprintf(`,app=%q`, apps[0])
			} else {
				sel += fmt.Sprintf(`,app=~%q`, strings.Join(apps, "|"))
			}
			multiPods = true
		case len(list) == 1:
			sel += fmt.Sprintf(`,pod=%q`, list[0])
		case len(list) > 1:
			sel += fmt.Sprintf(`,pod=~%q`, strings.Join(list, "|"))
			multiPods = true
		}
	} else if pod := q.Get("pod"); pod != "" {
		if q.Get("scope") == "service" {
			app, err := s.kube.PodAppLabel(r.Context(), user.Username, namespace, pod)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			if app == "" {
				http.Error(w, "could not resolve the service label of the pod", http.StatusBadRequest)
				return
			}
			sel += fmt.Sprintf(`,app=%q`, app)
			multiPods = true
		} else {
			sel += fmt.Sprintf(`,pod=%q`, pod)
		}
	}
	if c := q.Get("container"); c != "" {
		sel += fmt.Sprintf(`,container=%q`, c)
	}

	logql := "{" + sel + "}"
	// Free-text filter: case-insensitive substring, with regex metacharacters
	// escaped so a stray "(" cannot break the query.
	if f := q.Get("filter"); f != "" {
		logql += fmt.Sprintf(` |~ %q`, "(?i)"+regexp.QuoteMeta(f))
	}

	// from/to are unix milliseconds — the precision matters for the paging
	// cursor used by "load more".
	now := time.Now()
	start, end := now.Add(-time.Hour), now
	if v := q.Get("from"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			start = time.UnixMilli(n)
		}
	}
	if v := q.Get("to"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			end = time.UnixMilli(n)
		}
	}

	limit := 1000
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 5000 {
			limit = n
		}
	}

	entries, err := s.loki.QueryRange(r.Context(), logql, start, end, limit, "backward")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	// Each line is "<loki-timestamp-ms>\t<raw line>". The Loki timestamp is the
	// ingestion time, which the frontend renders in the viewer's timezone; the
	// application's own timestamp stays inside the line.
	var b strings.Builder
	for _, e := range entries {
		b.WriteString(strconv.FormatInt(e.TS.UnixMilli(), 10))
		b.WriteByte('\t')
		if multiPods && e.Pod != "" { // label each line when several pods are merged
			b.WriteString("[" + e.Pod + "] ")
		}
		b.WriteString(e.Line)
		b.WriteByte('\n')
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte(b.String()))
}
