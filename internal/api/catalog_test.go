package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"helm.sh/helm/v3/pkg/chart"

	"devops-tools/internal/audit"
	"devops-tools/internal/auth"
	"devops-tools/internal/config"
	"devops-tools/internal/helmcat"
	"devops-tools/internal/helmreq"
	"devops-tools/internal/kube"
	"devops-tools/internal/rbac"
	"devops-tools/internal/store"
)

type catalogFixture struct {
	h      http.Handler
	digest string
	mu     sync.Mutex
	// installed receives the values each install ran with.
	installed chan map[string]any
}

var catalogUsers = map[string][]string{"anna": {"dev"}, "bob": {"dev"}, "root": {"admin"}}

func newCatalogFixture(t *testing.T) *catalogFixture {
	t.Helper()
	model := &config.RBACConfig{Roles: map[string]config.RoleSpec{
		"dev": {Namespaces: []config.NamespaceGrant{{Namespace: "payments-dev",
			Operations: []string{OpHelmList, OpHelmInstall, OpHelmUpgrade}}}},
		"admin": {Namespaces: []config.NamespaceGrant{{Namespace: "*", Operations: []string{"*"}}},
			Global: []string{OpUserManage}},
	}}
	authz := rbac.New(model)
	authz.CloseToExec([]string{"kube-*", "devops-tools"})
	st, err := store.Open(filepath.Join(t.TempDir(), "access.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.SeedRoles(model); err != nil {
		t.Fatal(err)
	}
	for name, roles := range catalogUsers {
		if err := st.CreateLocalUser(name, "", "hash", roles); err != nil {
			t.Fatal(err)
		}
	}
	cfg := &config.Config{Environment: "test",
		Catalog: config.CatalogConfig{RequestTTL: 7 * 24 * time.Hour, InstallTimeout: time.Minute}}
	s := &Server{cfg: cfg, authz: authz, audit: audit.New(), store: st,
		operations: append(config.NamespaceOperations(), config.GlobalOperations()...)}
	f := &catalogFixture{digest: "sha256:aaa", installed: make(chan map[string]any, 4)}

	s.catalog = &catalog{
		requests: helmreq.NewMemory(),
		fetch: func(_ context.Context, repo, name, version string) (*helmcat.Located, *helmcat.Archive, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			ch := &chart.Chart{Metadata: &chart.Metadata{Name: name, Version: version}}
			return &helmcat.Located{Package: helmcat.Package{Repo: repo, Name: name, Version: version}},
				&helmcat.Archive{Chart: ch, Digest: f.digest}, nil
		},
		planner: func(_ context.Context, _, _ string, _ *chart.Chart, vals map[string]any, _ bool) (*kube.ReleasePlan, error) {
			p := &kube.ReleasePlan{Objects: []kube.PlanObject{{Kind: "StatefulSet", Name: "redis"}, {Kind: "Service", Name: "a"}, {Kind: "Service", Name: "b"}},
				Images: []string{"bitnami/redis:8.2"}}
			if vals["bad"] != nil {
				p.Problems = append(p.Problems, kube.PlanProblem{Level: "error", Text: "ClusterRole x is cluster-wide"})
			}
			if img, ok := vals["image"].(string); ok {
				p.Images = []string{img}
			}
			return p, nil
		},
		images: func(_ context.Context, images []string) []helmcat.ImageState {
			out := []helmcat.ImageState{}
			for _, im := range images {
				out = append(out, helmcat.ImageState{Image: im, Missing: strings.Contains(im, "gone")})
			}
			return out
		},
		installer: func(_ context.Context, _ *helmreq.Request, _ *chart.Chart, vals map[string]any) error {
			f.installed <- vals
			return nil
		},
		current: func(string, string) (*kube.CatalogRelease, error) { return nil, kube.ErrNotFromCatalog },
		trouble: func(context.Context, string, string) string { return "" },
		releases: func(string) ([]kube.HelmRelease, error) {
			return []kube.HelmRelease{{Name: "redis", Catalog: &kube.CatalogOrigin{Repo: "bitnami", Chart: "redis"}}, {Name: "payments-api"}}, nil
		},
		namespaces: func(context.Context) ([]string, error) {
			return []string{"devops-tools", "payments-dev", "accounts-dev"}, nil
		},
		nsExists: func(_ context.Context, ns string) (bool, error) {
			return ns == "payments-dev" || ns == "accounts-dev" || ns == "devops-tools", nil
		},
		templates: helmreq.NewMemoryTemplates(),
		hints: func(context.Context, string) *kube.ClusterHints {
			return &kube.ClusterHints{StorageClasses: []kube.StorageClassHint{{Name: "local-path", Default: true}}}
		},
		fromCatalog: func() ([]kube.HelmRelease, error) {
			return []kube.HelmRelease{
				{Name: "redis", Namespace: "payments-dev", Catalog: &kube.CatalogOrigin{Repo: "bitnami", Chart: "redis"}},
				{Name: "kafka", Namespace: "accounts-dev", Catalog: &kube.CatalogOrigin{Repo: "bitnami", Chart: "kafka"}},
			}, nil
		},
	}

	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			name := req.Header.Get("X-User")
			ctx := auth.WithUser(req.Context(), &auth.User{Username: name, Roles: catalogUsers[name]})
			next.ServeHTTP(w, req.WithContext(ctx))
		})
	})
	r.Route("/api", func(r chi.Router) { s.catalogRoutes(r, "/namespaces/{namespace}") })
	f.h = r
	return f
}

func (f *catalogFixture) call(t *testing.T, user, method, path string, body any) (int, map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("X-User", user)
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	out := map[string]any{}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func (f *catalogFixture) waitInstall(t *testing.T) map[string]any {
	t.Helper()
	select {
	case v := <-f.installed:
		return v
	case <-time.After(5 * time.Second):
		t.Fatal("nothing was installed")
		return nil
	}
}

func ask(release, values string) map[string]string {
	return map[string]string{"kind": "install", "release": release, "repo": "bitnami", "chart": "redis",
		"version": "21.2.3", "values": values}
}

const reqs = "/api/namespaces/payments-dev/helm/requests"

func TestRequestApprovedWithChangedValues(t *testing.T) {
	f := newCatalogFixture(t)

	code, q := f.call(t, "anna", "POST", reqs, ask("cache", "memory: 512Mi\n"))
	if code != http.StatusCreated || q["status"] != helmreq.StatusWaiting {
		t.Fatalf("request: %d %v", code, q)
	}
	if objs, _ := q["objects"].([]any); len(objs) != 2 || objs[1] != "Service ×2" {
		t.Errorf("objects = %v", q["objects"])
	}
	id := q["id"].(string)

	// Nothing installs before an administrator answers, and a developer
	// cannot answer.
	if code, _ := f.call(t, "anna", "POST", "/api/helm/requests/"+id+"/approve", map[string]any{}); code != http.StatusForbidden {
		t.Errorf("a developer approving: %d", code)
	}
	if _, c := f.call(t, "root", "GET", "/api/helm/requests/count", nil); c["waiting"] != float64(1) {
		t.Errorf("count = %v", c)
	}
	// One request per release at a time.
	if code, _ := f.call(t, "bob", "POST", reqs, ask("cache", "")); code != http.StatusConflict {
		t.Errorf("a second request for the same release: %d", code)
	}

	code, q = f.call(t, "root", "POST", "/api/helm/requests/"+id+"/approve", map[string]any{"values": "memory: 1Gi\n"})
	if code != http.StatusOK || q["status"] != helmreq.StatusInstalling || q["askedValues"] != "memory: 512Mi\n" {
		t.Fatalf("approve: %d %v", code, q)
	}
	if vals := f.waitInstall(t); vals["memory"] != "1Gi" {
		t.Errorf("installed with %v, want the administrator's values", vals)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		_, list := f.callList(t, "anna", "/api/helm/my-requests")
		if len(list) == 1 && list[0]["status"] == helmreq.StatusInstalled {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Error("the request never read as installed")
}

func (f *catalogFixture) callList(t *testing.T, user, path string) (int, []map[string]any) {
	t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	req.Header.Set("X-User", user)
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	var out []map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func TestRejectWithdrawAndOwnership(t *testing.T) {
	f := newCatalogFixture(t)
	_, q := f.call(t, "anna", "POST", reqs, ask("cache", ""))
	id := q["id"].(string)

	if code, _ := f.call(t, "bob", "DELETE", reqs+"/"+id, nil); code != http.StatusForbidden {
		t.Errorf("someone else withdrawing: %d", code)
	}
	if code, _ := f.call(t, "root", "POST", "/api/helm/requests/"+id+"/reject", map[string]string{"comment": " "}); code != http.StatusBadRequest {
		t.Errorf("rejecting without a reason: %d", code)
	}
	code, q := f.call(t, "root", "POST", "/api/helm/requests/"+id+"/reject", map[string]string{"comment": "use the shared one"})
	if code != http.StatusOK || q["status"] != helmreq.StatusRejected || q["comment"] != "use the shared one" {
		t.Fatalf("reject: %d %v", code, q)
	}
	if code, _ := f.call(t, "anna", "DELETE", reqs+"/"+id, nil); code != http.StatusConflict {
		t.Errorf("withdrawing an answered request: %d", code)
	}

	_, q = f.call(t, "anna", "POST", reqs, ask("cache", ""))
	if code, q := f.call(t, "anna", "DELETE", reqs+"/"+q["id"].(string), nil); code != http.StatusOK || q["status"] != helmreq.StatusWithdrawn {
		t.Errorf("withdraw: %d %v", code, q)
	}
}

func TestAdministratorInstallsDirectly(t *testing.T) {
	f := newCatalogFixture(t)
	code, q := f.call(t, "root", "POST", reqs, ask("cache", "a: 1\n"))
	if code != http.StatusCreated || q["status"] != helmreq.StatusInstalling || q["decider"] != "root" {
		t.Fatalf("direct install: %d %v", code, q)
	}
	f.waitInstall(t)
}

func TestRequestsThatDoNotPass(t *testing.T) {
	f := newCatalogFixture(t)

	if code, out := f.call(t, "anna", "POST", reqs, ask("cache", "bad: true\n")); code != http.StatusUnprocessableEntity {
		t.Errorf("a failing check: %d %v", code, out)
	}
	// An image not found is the administrator's to weigh — the nodes may pull
	// it from a mirror — so the request goes through, with the warning on it.
	if code, out := f.call(t, "anna", "POST", reqs, ask("gone", "image: bitnami/gone:1\n")); code != http.StatusCreated {
		t.Errorf("an image that is not there should only warn: %d %v", code, out)
	}
	if code, _ := f.call(t, "anna", "POST", reqs, ask("Not_A_Name", "")); code != http.StatusBadRequest {
		t.Errorf("a bad release name: %d", code)
	}
	if code, _ := f.call(t, "anna", "POST", reqs, ask("cache", "a: [")); code != http.StatusBadRequest {
		t.Errorf("values that are not YAML: %d", code)
	}
	// The portal's own namespace is closed to installs, "*" or not.
	if code, _ := f.call(t, "root", "POST", "/api/namespaces/devops-tools/helm/requests", ask("cache", "")); code != http.StatusForbidden {
		t.Errorf("installing beside the portal: %d", code)
	}

	// The author replaces the version between the request and the answer.
	_, q := f.call(t, "anna", "POST", reqs, ask("cache", ""))
	f.mu.Lock()
	f.digest = "sha256:bbb"
	f.mu.Unlock()
	code, out := f.call(t, "root", "POST", "/api/helm/requests/"+q["id"].(string)+"/approve", map[string]any{})
	if code != http.StatusBadRequest || !strings.Contains(out["error"].(string), "no longer the archive") {
		t.Errorf("approving a replaced archive: %d %v", code, out)
	}
}

func TestValueKeysNamesOnly(t *testing.T) {
	vals, _ := parseValues("auth:\n  password: s3cret\nreplicas: 2\n")
	keys := valueKeys(vals)
	if strings.Join(keys, ",") != "auth.password,replicas" {
		t.Errorf("keys = %v", keys)
	}
	q := &helmreq.Request{Chart: "redis", Version: "1", Repo: "bitnami"}
	if d := requestDetail(q, vals); strings.Contains(d, "s3cret") {
		t.Errorf("the audit line carries a value: %s", d)
	}
}

func TestPackagesAcrossNamespaces(t *testing.T) {
	f := newCatalogFixture(t)
	_, out := f.call(t, "anna", "GET", "/api/helm/namespaces", nil)
	if got, _ := json.Marshal(out["namespaces"]); string(got) != `["payments-dev"]` {
		t.Errorf("a developer's namespaces = %s", got)
	}
	// "*" is every namespace but the closed ones.
	_, out = f.call(t, "root", "GET", "/api/helm/namespaces", nil)
	if got, _ := json.Marshal(out["namespaces"]); string(got) != `["accounts-dev","payments-dev"]` {
		t.Errorf("an administrator's namespaces = %s", got)
	}

	// Installed: only the namespaces one may ask in.
	_, list := f.callList(t, "anna", "/api/helm/catalog-releases")
	if len(list) != 1 || list[0]["name"] != "redis" || list[0]["mayUpgrade"] != true || list[0]["mayList"] != true {
		t.Errorf("a developer's catalog releases: %v", list)
	}
	_, list = f.callList(t, "root", "/api/helm/catalog-releases")
	if len(list) != 2 {
		t.Errorf("an administrator's catalog releases: %v", list)
	}

	// Requests: the team's, from every namespace the caller works in.
	_, q := f.call(t, "root", "POST", "/api/namespaces/accounts-dev/helm/requests", ask("kafka", ""))
	f.waitInstall(t)
	_, q2 := f.call(t, "anna", "POST", reqs, ask("cache", ""))
	_, mine := f.callList(t, "bob", "/api/helm/my-requests")
	if len(mine) != 1 || mine[0]["id"] != q2["id"] {
		t.Errorf("bob sees %v, want anna's request in payments-dev only (not %v)", mine, q["id"])
	}

	// Someone who may ask nowhere does not get the page at all.
	catalogUsers["nobody"] = nil
	defer delete(catalogUsers, "nobody")
	if code, _ := f.call(t, "nobody", "GET", "/api/helm/catalog?q=redis", nil); code != http.StatusForbidden {
		t.Errorf("the catalog for someone with no grant: %d", code)
	}
}

func TestFillValues(t *testing.T) {
	f := newCatalogFixture(t)
	code, out := f.call(t, "anna", "POST", "/api/helm/values/fill", map[string]any{
		"values": "replicas: 2\n", "storageClass": "local-path", "storagePaths": []string{"global.defaultStorageClass"},
	})
	if code != http.StatusOK || !strings.Contains(out["values"].(string), "defaultStorageClass: local-path") ||
		!strings.Contains(out["values"].(string), "replicas: 2") {
		t.Errorf("fill: %d %v", code, out)
	}
	if code, _ := f.call(t, "anna", "POST", "/api/helm/values/fill", map[string]any{"values": "a: ["}); code != http.StatusBadRequest {
		t.Errorf("values that are not YAML: %d", code)
	}
}

// A new namespace: asked for by a developer who holds nothing in it, made on
// approval, and given to the requester's role so they keep sight of it.
func TestRequestIntoANewNamespace(t *testing.T) {
	f := newCatalogFixture(t)
	newReqs := "/api/namespaces/team-new/helm/requests"
	body := func(ns bool) map[string]any {
		return map[string]any{"kind": "install", "release": "cache", "repo": "bitnami", "chart": "redis",
			"version": "21.2.3", "values": "", "newNamespace": ns}
	}

	if code, _ := f.call(t, "anna", "POST", newReqs, body(false)); code != http.StatusForbidden {
		t.Errorf("a namespace not theirs, not asked as new: %d", code)
	}
	for ns, why := range map[string]string{"payments-dev": "exists", "kube-tools": "reserved", "devops-tools": "the portal's", "Bad_Name": "not a name"} {
		if code, _ := f.call(t, "anna", "POST", "/api/namespaces/"+ns+"/helm/requests", body(true)); code != http.StatusBadRequest {
			t.Errorf("%s (%s): %d", ns, why, code)
		}
	}

	code, q := f.call(t, "anna", "POST", newReqs, body(true))
	if code != http.StatusCreated || q["newNamespace"] != true {
		t.Fatalf("request: %d %v", code, q)
	}
	id := q["id"].(string)

	// Their own request stays in sight, though the namespace is not theirs.
	req := httptest.NewRequest("GET", "/api/helm/my-requests", nil)
	req.Header.Set("X-User", "anna")
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), id) {
		t.Errorf("my requests: %s", rec.Body.String())
	}

	// The approval offers the requester's roles, with what each would get.
	req = httptest.NewRequest("GET", "/api/helm/requests/"+id+"/grants", nil)
	req.Header.Set("X-User", "root")
	rec = httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	var grants []roleGrant
	_ = json.Unmarshal(rec.Body.Bytes(), &grants)
	if len(grants) != 1 || grants[0].Role != "dev" || grants[0].Covered || len(grants[0].Operations) != 3 {
		t.Fatalf("grants = %+v", grants)
	}

	if code, out := f.call(t, "root", "POST", "/api/helm/requests/"+id+"/approve", map[string]any{"grant": []string{"admin"}}); code != http.StatusBadRequest {
		t.Errorf("a role the requester does not hold: %d %v", code, out)
	}
	if code, out := f.call(t, "root", "POST", "/api/helm/requests/"+id+"/approve", map[string]any{"grant": []string{"dev"}}); code != http.StatusOK {
		t.Fatalf("approve: %d %v", code, out)
	}
	f.waitInstall(t)
	if code, _ := f.call(t, "anna", "POST", "/api/namespaces/team-new/helm/check", ask("other", "")); code == http.StatusForbidden {
		t.Error("the role should reach the new namespace now")
	}
}

func TestStartingValues(t *testing.T) {
	f := newCatalogFixture(t)
	path := "/api/helm/templates/bitnami/redis"
	if code, _ := f.call(t, "anna", "PUT", path, map[string]string{"values": "a: 1"}); code != http.StatusForbidden {
		t.Errorf("a developer saving starting values: %d", code)
	}
	if code, _ := f.call(t, "root", "PUT", path, map[string]string{"values": "a: ["}); code != http.StatusBadRequest {
		t.Errorf("values that are not YAML: %d", code)
	}
	code, out := f.call(t, "root", "PUT", path, map[string]string{"values": "global:\n  domain: example.com\n", "note": "our domain"})
	if code != http.StatusOK || out["author"] != "root" {
		t.Fatalf("save: %d %v", code, out)
	}
	_, info := f.call(t, "anna", "GET", "/api/helm/catalog/bitnami/redis?version=21.2.3", nil)
	if tpl, _ := info["template"].(map[string]any); tpl == nil || tpl["note"] != "our domain" {
		t.Errorf("the chart should come with its starting values: %v", info["template"])
	}
	if code, _ := f.call(t, "root", "DELETE", path, nil); code != http.StatusOK {
		t.Errorf("remove: %d", code)
	}
	if _, info := f.call(t, "anna", "GET", "/api/helm/catalog/bitnami/redis?version=21.2.3", nil); info["template"] != nil {
		t.Errorf("removed, still there: %v", info["template"])
	}
}
