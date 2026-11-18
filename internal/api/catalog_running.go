package api

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"devops-tools/internal/audit"
	"devops-tools/internal/auth"
	"devops-tools/internal/helmreq"
	"devops-tools/internal/kube"
)

// An install waits for its pods, up to the configured timeout. Meanwhile the
// request shows how far the pods have come and what holds the slow ones, and
// whoever asked — or an administrator — can stop it rather than sit it out.

// runningInstall is an install under way in this process.
type runningInstall struct {
	namespace, release string
	cancel             context.CancelFunc
	// finished is closed when the install has returned.
	finished chan struct{}

	mu        sync.Mutex
	stoppedBy string
	why       string
}

func (r *runningInstall) stop(by, why string) {
	r.mu.Lock()
	if r.stoppedBy == "" {
		r.stoppedBy, r.why = by, why
	}
	r.mu.Unlock()
	r.cancel()
}

func (r *runningInstall) stopped() (by, why string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.stoppedBy, r.why
}

// installs are the installs under way, by request.
type installs struct {
	mu  sync.Mutex
	run map[string]*runningInstall
}

func (in *installs) add(id string, r *runningInstall) {
	in.mu.Lock()
	defer in.mu.Unlock()
	if in.run == nil {
		in.run = map[string]*runningInstall{}
	}
	in.run[id] = r
}

func (in *installs) done(id string) {
	in.mu.Lock()
	defer in.mu.Unlock()
	delete(in.run, id)
}

func (in *installs) get(id string) *runningInstall {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.run[id]
}

// of is the install under way for a release, or nil.
func (in *installs) of(ns, release string) *runningInstall {
	in.mu.Lock()
	defer in.mu.Unlock()
	for _, r := range in.run {
		if r.namespace == ns && r.release == release {
			return r
		}
	}
	return nil
}

// requestView is a request as it is shown: what it holds, without what its
// Secrets hold, and while it installs, how far it has come.
type requestView struct {
	*helmreq.Request
	Progress *kube.InstallProgress `json:"progress,omitempty"`
	// MayStop: the caller can stop the install under way.
	MayStop bool `json:"mayStop,omitempty"`
}

// viewRequests shows requests to a caller. Installs that nothing runs any
// more — the portal restarted under them — are settled first.
func (s *Server) viewRequests(ctx context.Context, user *auth.User, list []*helmreq.Request) []requestView {
	out := make([]requestView, 0, len(list))
	for _, q := range list {
		if q.Status == helmreq.StatusInstalling {
			q = s.settleOrphan(ctx, q)
		}
		v := requestView{Request: q.Redacted()}
		if q.Status == helmreq.StatusInstalling {
			v.Progress = s.catalog.progress(ctx, q.Namespace, q.Release)
			v.MayStop = user != nil && (q.Requester == user.Username || s.isApprover(user.Roles))
		}
		out = append(out, v)
	}
	return out
}

// settleOrphan ends a request left installing by a process that is gone:
// past the time an install may take and not running here, it ended as the
// release did.
func (s *Server) settleOrphan(ctx context.Context, q *helmreq.Request) *helmreq.Request {
	if s.catalog.running.get(q.ID) != nil || q.Decided == nil ||
		time.Since(*q.Decided) < s.cfg.Catalog.InstallTimeout+2*time.Minute {
		return q
	}
	status, detail := helmreq.StatusFailed, "the portal restarted during the install and did not see it end"
	if rels, err := s.catalog.releases(q.Namespace); err == nil {
		for _, rel := range rels {
			if rel.Name != q.Release {
				continue
			}
			if rel.Status == "deployed" {
				status, detail = helmreq.StatusInstalled, ""
			} else {
				detail += "; the release is " + rel.Status
			}
		}
	}
	upd, err := s.catalog.requests.Update(ctx, q.ID, func(r *helmreq.Request) error {
		if r.Status != helmreq.StatusInstalling {
			return nil
		}
		r.Status, r.Detail = status, detail
		return nil
	})
	if err != nil {
		slog.Error("settle an install nothing runs", "request", q.ID, "err", err)
		return q
	}
	return upd
}

// handleStopInstall stops an install under way: the release is left failed,
// to be uninstalled or tried again.
func (s *Server) handleStopInstall(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.FromContext(r.Context())
	ns, id := chi.URLParam(r, "namespace"), chi.URLParam(r, "id")
	q, err := s.catalog.requests.Get(r.Context(), id)
	if err != nil || q.Namespace != ns {
		requestError(w, helmreq.ErrNotFound)
		return
	}
	if q.Requester != user.Username && !s.isApprover(user.Roles) {
		requestError(w, fmt.Errorf("only the requester or an administrator can stop an install: %w", errForbidden))
		return
	}
	if q.Status != helmreq.StatusInstalling {
		requestError(w, fmt.Errorf("the request is already %s", q.Status))
		return
	}
	if run := s.catalog.running.get(id); run != nil {
		run.stop(user.Username, "")
	} else {
		// Nothing here runs it: say so on the request.
		q, err = s.catalog.requests.Update(r.Context(), id, func(q *helmreq.Request) error {
			if q.Status == helmreq.StatusInstalling {
				q.Status, q.Detail = helmreq.StatusFailed, "stopped by "+user.Username+"; nothing was running it any more"
			}
			return nil
		})
		if err != nil {
			requestError(w, err)
			return
		}
	}
	s.audit.Log(audit.Entry{User: user.Username, Environment: s.cfg.Environment, Namespace: ns,
		Operation: "helm-" + q.Kind + "-stop", Target: q.Release, Allowed: true, Success: true,
		Detail: q.Chart + " " + q.Version})
	writeJSON(w, http.StatusOK, map[string]string{"status": "stopping"})
}

// stopInstallOf stops an install under way for a release about to be
// removed, so the two do not race.
func (s *Server) stopInstallOf(ns, release, by string) {
	if s.catalog == nil {
		return
	}
	if run := s.catalog.running.of(ns, release); run != nil {
		run.stop(by, "to uninstall it")
		select {
		case <-run.finished:
		case <-time.After(10 * time.Second):
		}
	}
}

// markUninstalled notes on a release's requests that it was removed: what
// they ended as is history now.
func (s *Server) markUninstalled(ctx context.Context, ns, release, by string) {
	if s.catalog == nil {
		return
	}
	all, err := s.catalog.requests.List(ctx)
	if err != nil {
		return
	}
	now := time.Now().UTC()
	for _, q := range all {
		if q.Namespace != ns || q.Release != release || q.Uninstalled != nil ||
			(q.Status != helmreq.StatusInstalled && q.Status != helmreq.StatusFailed && q.Status != helmreq.StatusInstalling) {
			continue
		}
		if _, err := s.catalog.requests.Update(ctx, q.ID, func(r *helmreq.Request) error {
			if r.Uninstalled == nil {
				r.Uninstalled, r.UninstalledBy = &now, by
				if r.Status == helmreq.StatusInstalling {
					r.Status, r.Detail = helmreq.StatusFailed, "stopped by "+by+" to uninstall it"
				}
			}
			return nil
		}); err != nil {
			slog.Error("note an uninstall on a request", "request", q.ID, "err", err)
		}
	}
}

// failureSummary says in a few words why an install failed; Helm's own
// words stay on the request beside it.
func failureSummary(raw string, timeout time.Duration) string {
	low := strings.ToLower(raw)
	switch {
	case strings.Contains(low, "wait for resources failed") || strings.Contains(low, "timed out waiting") ||
		strings.Contains(low, "context deadline exceeded"):
		within := timeout.String()
		if timeout >= time.Minute && timeout%time.Minute == 0 {
			within = fmt.Sprintf("%d min", int(timeout.Minutes()))
		}
		return "the pods were not ready within " + within
	case strings.Contains(low, "context canceled"):
		return "the install was stopped"
	}
	return strings.TrimPrefix(strings.TrimPrefix(raw, "INSTALLATION FAILED: "), "UPGRADE FAILED: ")
}
