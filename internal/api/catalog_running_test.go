package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"helm.sh/helm/v3/pkg/chart"

	"devops-tools/internal/helmreq"
	"devops-tools/internal/kube"
)

// An install waiting for pods shows how far they came, and whoever asked can
// stop it instead of sitting out the timeout.
func TestStopAnInstall(t *testing.T) {
	f := newCatalogFixture(t)
	started := make(chan struct{}, 1)
	f.s.catalog.installer = func(ctx context.Context, _ *helmreq.Request, _ *chart.Chart, _ map[string]any) error {
		started <- struct{}{}
		<-ctx.Done()
		return errors.New("context canceled")
	}
	f.s.catalog.trouble = func(context.Context, string, string) string {
		return "redis-master-0: Unhealthy — Readiness probe failed"
	}
	_, q := f.call(t, "anna", "POST", reqs, ask("cache", ""))
	id := q["id"].(string)
	if code, _ := f.call(t, "root", "POST", "/api/helm/requests/"+id+"/approve", map[string]any{}); code != http.StatusOK {
		t.Fatalf("approve: %d", code)
	}
	<-started

	_, list := f.callList(t, "anna", "/api/helm/my-requests")
	if len(list) != 1 || list[0]["mayStop"] != true || list[0]["progress"] == nil {
		t.Fatalf("an install under way: %v", list)
	}
	if code, _ := f.call(t, "bob", "POST", reqs+"/"+id+"/stop", nil); code != http.StatusForbidden {
		t.Errorf("somebody else stopping it: %d", code)
	}
	if code, _ := f.call(t, "anna", "POST", reqs+"/"+id+"/stop", nil); code != http.StatusOK {
		t.Fatalf("stop: %d", code)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		got, _ := f.s.catalog.requests.Get(context.Background(), id)
		if got.Status == helmreq.StatusFailed {
			if !strings.Contains(got.Detail, "stopped by anna") || !strings.Contains(got.Detail, "Readiness probe failed") {
				t.Errorf("detail %q", got.Detail)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("still %s", got.Status)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A request left installing by a portal that restarted ends as its release
// did, rather than installing for ever.
func TestOrphanedInstallSettles(t *testing.T) {
	f := newCatalogFixture(t)
	long := time.Now().Add(-time.Hour)
	q := &helmreq.Request{ID: "x1", Kind: helmreq.KindInstall, Namespace: "payments-dev", Release: "redis",
		Repo: "bitnami", Chart: "redis", Requester: "anna", Created: long, Expires: long.Add(time.Hour),
		Status: helmreq.StatusInstalling, Decider: "root", Decided: &long}
	if err := f.s.catalog.requests.Create(context.Background(), q); err != nil {
		t.Fatal(err)
	}
	f.s.catalog.releases = func(string) ([]kube.HelmRelease, error) {
		return []kube.HelmRelease{{Name: "redis", Status: "pending-install"}}, nil
	}
	_, list := f.callList(t, "anna", "/api/helm/my-requests")
	if len(list) != 1 || list[0]["status"] != helmreq.StatusFailed || !strings.Contains(list[0]["detail"].(string), "pending-install") {
		t.Errorf("orphan: %v", list)
	}
}

// A release removed through the portal leaves its requests as history: how
// they ended is no longer a problem to look at.
func TestUninstallMarksRequests(t *testing.T) {
	f := newCatalogFixture(t)
	now := time.Now()
	for i, st := range []string{helmreq.StatusFailed, helmreq.StatusInstalled, helmreq.StatusRejected} {
		q := &helmreq.Request{ID: "u" + string(rune('0'+i)), Namespace: "payments-dev", Release: "redis", Status: st,
			Created: now, Expires: now.Add(time.Hour)}
		if err := f.s.catalog.requests.Create(context.Background(), q); err != nil {
			t.Fatal(err)
		}
	}
	f.s.markUninstalled(context.Background(), "payments-dev", "redis", "root")
	for id, want := range map[string]bool{"u0": true, "u1": true, "u2": false} {
		q, _ := f.s.catalog.requests.Get(context.Background(), id)
		if (q.Uninstalled != nil) != want || (want && q.UninstalledBy != "root") {
			t.Errorf("%s (%s): uninstalled %v by %q", id, q.Status, q.Uninstalled, q.UninstalledBy)
		}
	}
}

func TestFailureSummary(t *testing.T) {
	raw := `context deadline exceeded: Error received when checking status of resource redis-headless; wait for resources failed after 5m0s: context deadline exceeded`
	if got := failureSummary(raw, 5*time.Minute); got != "the pods were not ready within 5 min" {
		t.Errorf("%q", got)
	}
	if got := failureSummary("INSTALLATION FAILED: services \"x\" already exists", time.Minute); got != `services "x" already exists` {
		t.Errorf("%q", got)
	}
}
