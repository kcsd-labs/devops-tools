package helmcat

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chartutil"
)

func TestSplitImage(t *testing.T) {
	cases := []struct{ in, repo, ref string }{
		{"redis", "registry-1.docker.io/library/redis", "latest"},
		{"redis:7.2", "registry-1.docker.io/library/redis", "7.2"},
		{"bitnami/redis:8.2", "registry-1.docker.io/bitnami/redis", "8.2"},
		{"docker.io/bitnami/redis:8.2", "registry-1.docker.io/bitnami/redis", "8.2"},
		{"quay.io/prometheus/node-exporter:v1.9.1", "quay.io/prometheus/node-exporter", "v1.9.1"},
		{"registry.local:5000/team/app", "registry.local:5000/team/app", "latest"},
		{"ghcr.io/x/y@sha256:abc", "ghcr.io/x/y", "sha256:abc"},
	}
	for _, c := range cases {
		repo, ref, err := splitImage(c.in)
		if err != nil || repo != c.repo || ref != c.ref {
			t.Errorf("%s: got %s %s %v, want %s %s", c.in, repo, ref, err, c.repo, c.ref)
		}
	}
}

// The hub's answers, as Artifact Hub writes them.
func fakeHub(t *testing.T, contentURL string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/packages/search":
			if r.URL.Query().Get("verified_publisher") != "true" || r.URL.Query().Get("kind") != "0" {
				t.Errorf("search should ask for verified Helm charts only: %s", r.URL.RawQuery)
			}
			w.Header().Set("Pagination-Total-Count", "41")
			_, _ = w.Write([]byte(`{"packages":[{"name":"redis","description":"cache","version":"21.2.3","app_version":"8.2",
				"logo_image_id":"abc-1","stars":500,"repository":{"name":"bitnami","display_name":"Bitnami","url":"https://charts.example.com"}}]}`))
		case r.URL.Path == "/api/v1/packages/helm/bitnami/redis/21.2.3":
			_, _ = w.Write([]byte(`{"name":"redis","version":"21.2.3","content_url":"` + contentURL + `","readme":"# Redis",
				"available_versions":[{"version":"21.2.3","app_version":"8.2"},{"version":"21.1.0"}],
				"repository":{"name":"bitnami","url":"https://charts.example.com"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestHubSearchAndLocate(t *testing.T) {
	hub := fakeHub(t, "oci://registry.example.com/charts/redis:21.2.3")
	defer hub.Close()
	h := NewHub(hub.URL)

	page, err := h.Search(context.Background(), "redis", 0)
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 41 || len(page.Packages) != 1 {
		t.Fatalf("page = %+v", page)
	}
	p := page.Packages[0]
	if p.Repo != "bitnami" || p.RepoTitle != "Bitnami" || p.Logo != hub.URL+"/image/abc-1" {
		t.Errorf("package = %+v", p)
	}

	loc, err := h.Locate(context.Background(), "bitnami", "redis", "21.2.3")
	if err != nil {
		t.Fatal(err)
	}
	if loc.ContentURL != "oci://registry.example.com/charts/redis:21.2.3" || len(loc.Versions) != 2 || loc.Readme != "# Redis" ||
		loc.Page != hub.URL+"/packages/helm/bitnami/redis" {
		t.Errorf("located = %+v", loc)
	}
	if _, err := h.Locate(context.Background(), "bitnami", "nope", "1.0.0"); err != ErrNotFound {
		t.Errorf("unknown chart: %v, want ErrNotFound", err)
	}
	// Names go into a path: anything that is not a name never reaches it.
	if _, err := h.Locate(context.Background(), "../..", "redis", ""); err != ErrNotFound {
		t.Errorf("a path for a repository name: %v", err)
	}
}

func demoArchive(t *testing.T) []byte {
	t.Helper()
	ch := &chart.Chart{
		Metadata:  &chart.Metadata{APIVersion: "v2", Name: "demo", Version: "1.0.0"},
		Values:    map[string]any{"replicas": 1},
		Templates: []*chart.File{{Name: "templates/cm.yaml", Data: []byte("kind: ConfigMap\n")}},
	}
	path, err := chartutil.Save(ch, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestFetchLoadsAndDigests(t *testing.T) {
	data := demoArchive(t)
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		_, _ = w.Write(data)
	}))
	defer srv.Close()

	f := NewFetcher()
	a, err := f.Fetch(context.Background(), srv.URL+"/demo-1.0.0.tgz")
	if err != nil {
		t.Fatal(err)
	}
	if a.Chart.Name() != "demo" || !strings.HasPrefix(a.Digest, "sha256:") || len(a.Digest) != 71 {
		t.Errorf("archive = %s %s", a.Chart.Name(), a.Digest)
	}
	again, _ := f.Fetch(context.Background(), srv.URL+"/demo-1.0.0.tgz")
	if hits != 1 || again.Digest != a.Digest {
		t.Errorf("a second fetch of the same version should come from the cache (hits %d)", hits)
	}
	if _, err := f.Fetch(context.Background(), "file:///etc/passwd"); err == nil {
		t.Error("only http, https and oci addresses are fetched")
	}
}
