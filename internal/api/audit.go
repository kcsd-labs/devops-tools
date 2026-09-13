package api

import (
	"net/http"
	"strconv"
	"time"

	"devops-tools/internal/auditlog"
	"devops-tools/internal/auth"
	"devops-tools/internal/config"
)

// auditScope works out what this caller may see.
//
// The two grants answer different questions. In a namespace, audit-read shows
// everything that happened there, whoever did it — otherwise "who restarted my
// service" has no answer, which is half the point of the page. Globally it
// shows the entries that belong to no namespace: sign-ins, refused sign-ins,
// and who was granted what.
func (s *Server) auditScope(user *auth.User) auditlog.Scope {
	if user == nil {
		return auditlog.Scope{}
	}
	sc := auditlog.Scope{Global: s.authz.AllowedGlobal(user.Roles, config.OpAuditRead)}
	for _, ns := range s.authz.NamespacesForOp(user.Roles, config.OpAuditRead) {
		if ns == "*" {
			sc.All = true
			sc.Namespaces = nil
			break
		}
		sc.Namespaces = append(sc.Namespaces, ns)
	}
	return sc
}

// handleAuditLog serves one page of the trail, newest first.
//
// There is no requireOp wrapper: the permission is per namespace and the page
// spans several, so the scope is worked out here and pushed down into the
// query. Filtering a page after the fact would return short pages and make the
// paging cursor lie about where it stopped.
func (s *Server) handleAuditLog(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.FromContext(r.Context())
	if s.auditLog == nil || s.auditLog.Source() == auditlog.SourceNone {
		http.Error(w, "the audit page needs Loki, or this service running in a cluster where it can read its own pod logs", http.StatusServiceUnavailable)
		return
	}
	scope := s.auditScope(user)
	if scope.Empty() {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	q := r.URL.Query()
	f := auditlog.Filter{
		Operation: q.Get("operation"),
		User:      q.Get("user"),
		Text:      q.Get("filter"),
	}
	if v := q.Get("to"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			f.To = time.UnixMilli(n)
		}
	}
	// The period the reader picked. It can only narrow what the horizon already
	// allows, so a stale page asking for more than the deployment keeps gets the
	// horizon rather than an error.
	if v := q.Get("from"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			f.From = time.UnixMilli(n)
		}
	}
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			f.Limit = n
		}
	}

	page, err := s.auditLog.Read(r.Context(), scope, f)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	entries := make([]map[string]any, 0, len(page.Entries))
	for _, e := range page.Entries {
		entries = append(entries, map[string]any{
			"time":      e.Time.UTC().Format(time.RFC3339Nano),
			"user":      e.User,
			"namespace": e.Namespace,
			"operation": e.Operation,
			"target":    e.Target,
			"allowed":   e.Allowed,
			"success":   e.Success,
			"error":     e.Error,
		})
	}

	var next int64
	if !page.Next.IsZero() {
		next = page.Next.UnixMilli()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"entries": entries,
		// Zero means the horizon was reached: the interface stops offering more
		// rather than letting somebody page into an empty past.
		"next":    next,
		"source":  string(s.auditLog.Source()),
		"horizon": int64(s.auditLog.Horizon() / time.Second),
		// The vocabulary for the operation filter. Same list the role editor
		// uses, so the two never drift apart.
		"operations":    s.operations,
		"namespaces":    scope.Namespaces,
		"allNamespaces": scope.All,
		"global":        scope.Global,
		// Where events older than the horizon went, if anywhere. Shown when the
		// reader reaches the end of what this deployment keeps, because "that is
		// all there is" would otherwise be a lie.
		"archiveNote": s.cfg.Logs.ArchiveNote,
		"archiveURL":  s.cfg.Logs.ArchiveURL,
	})
}
