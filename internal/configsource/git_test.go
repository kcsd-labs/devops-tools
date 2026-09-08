package configsource

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"devops-tools/internal/gitlab"
)

// Paths above this package are relative to basePath, so that a grant written
// once means the same thing whichever source a deployment uses. If the base
// path leaked into them, every grant would have to be rewritten to move an
// installation from Consul to Git — and a grant on "abs" would silently match
// nothing.
func TestPathsAreRelativeToBasePathBothWays(t *testing.T) {
	// GetFile makes its two requests at the same time, so what the handler
	// records has to be guarded.
	var mu sync.Mutex
	var requested string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/tree") {
			body, _ := json.Marshal([]gitlab.TreeEntry{
				{Name: "abs", Type: "tree", Path: "config/abs"},
				{Name: "application.yml", Type: "blob", Path: "config/abs/application.yml"},
				{Name: "application.yml", Type: "blob", Path: "config/fx-jobs/application.yml"},
			})
			_, _ = w.Write(body)
			return
		}
		mu.Lock()
		if strings.Contains(r.URL.Path, "/files/") {
			requested = r.RequestURI
		}
		mu.Unlock()
		_, _ = w.Write([]byte(`{"content":"","encoding":"text","last_commit_id":"c1"}`))
	}))
	defer srv.Close()

	g := NewGit(gitlab.New(srv.URL, "t"), "grp/cfg", "dev", "config")

	entries, err := g.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	// The directory is dropped; the two files come back without "config/".
	want := []string{"abs/application.yml", "fx-jobs/application.yml"}
	if len(entries) != len(want) {
		t.Fatalf("got %d entries, want %d: %+v", len(entries), len(want), entries)
	}
	for i, w := range want {
		if entries[i].Path != w {
			t.Errorf("entries[%d].Path = %q, want %q", i, entries[i].Path, w)
		}
	}

	// And the base path is put back on the way out, or GitLab is asked for a
	// file that does not exist.
	if _, err := g.Get(context.Background(), "abs/application.yml"); err != nil {
		t.Fatalf("Get: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(requested, url.PathEscape("config/abs/application.yml")) {
		t.Errorf("asked GitLab for %s; the base path was not restored", requested)
	}
}

// An empty base path means the repository root, and must not produce a leading
// slash — GitLab would treat "/abs/application.yml" as a different file.
func TestEmptyBasePathAddressesTheRepositoryRoot(t *testing.T) {
	// GetFile makes its two requests at the same time, so what the handler
	// records has to be guarded.
	var mu sync.Mutex
	var requested string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		if strings.Contains(r.URL.Path, "/files/") {
			requested = r.RequestURI
		}
		mu.Unlock()
		_, _ = w.Write([]byte(`{"content":"","encoding":"text","last_commit_id":"c1"}`))
	}))
	defer srv.Close()

	g := NewGit(gitlab.New(srv.URL, "t"), "grp/cfg", "dev", "")
	if _, err := g.Get(context.Background(), "abs/application.yml"); err != nil {
		t.Fatalf("Get: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Contains(requested, url.PathEscape("/abs/application.yml")) {
		t.Errorf("asked GitLab for %s; an empty base path produced a leading slash", requested)
	}
}

// A stale version has to arrive above this package as ErrConflict, whichever
// source produced it — that is the whole point of the shared error, and the
// page reacts to it rather than to GitLab's or Consul's own reporting.
func TestConflictIsTranslated(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"message":"file has changed since you started editing it"}`))
	}))
	defer srv.Close()

	g := NewGit(gitlab.New(srv.URL, "t"), "grp/cfg", "dev", "config")
	err := g.Put(context.Background(), "abs/application.yml", "x", "old", Editor{Username: "ada"})
	if err != ErrConflict {
		t.Fatalf("Put err = %v, want ErrConflict", err)
	}
}

// The commit is attributed to the person in the portal, not to the token's
// owner. Otherwise every change in the repository's history belongs to a
// service account, and the history stops answering what it is kept for.
func TestCommitIsAttributedToTheEditor(t *testing.T) {
	var body map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	g := NewGit(gitlab.New(srv.URL, "t"), "grp/cfg", "dev", "config")
	err := g.Put(context.Background(), "abs/application.yml", "k: v\n", "c1",
		Editor{Username: "ada", Email: "ada@example.com"})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if body["author_name"] != "ada" || body["author_email"] != "ada@example.com" {
		t.Errorf("commit author = %q <%q>, want the portal user", body["author_name"], body["author_email"])
	}
	if body["branch"] != "dev" {
		t.Errorf("committed to branch %q, want dev", body["branch"])
	}
	if body["last_commit_id"] != "c1" {
		t.Errorf("last_commit_id = %q; without it GitLab cannot refuse a stale write", body["last_commit_id"])
	}
	if !strings.Contains(body["commit_message"], "ada") {
		t.Errorf("commit message %q does not say who made the change", body["commit_message"])
	}
}
