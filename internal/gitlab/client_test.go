package gitlab

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// A stale last_commit_id is the whole reason the editor sends one back, and
// GitLab does not report it with 409 — it answers 400 with prose. Getting this
// wrong means a conflict is reported as an unknown failure, and the natural
// next move ("try again") is exactly the one that discards the other person's
// commit.
func TestStaleCommitIsRecognisedDespiteThe400(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"message":"You are attempting to update a file that has changed since you started editing it."}`))
	}))
	defer srv.Close()

	err := New(srv.URL, "t").PutFile(context.Background(), "g/p", "config/a.yml", "dev", "x", Commit{})
	if err != ErrConflict {
		t.Fatalf("PutFile err = %v, want ErrConflict", err)
	}
}

// Any other 400 must not be mistaken for a conflict: reporting "somebody else
// changed it" when the request was simply malformed sends people looking for a
// colleague who does not exist.
func TestOtherBadRequestIsNotAConflict(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"branch is missing"}`))
	}))
	defer srv.Close()

	err := New(srv.URL, "t").PutFile(context.Background(), "g/p", "config/a.yml", "dev", "x", Commit{})
	if err == ErrConflict {
		t.Fatal("a malformed request was reported as a conflict")
	}
	if err == nil {
		t.Fatal("a 400 was reported as success")
	}
}

// A rejected token looks like nothing else and should say so, rather than
// leaving "status 401" to be interpreted as a missing repository.
func TestRejectedTokenSaysSo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	_, err := New(srv.URL, "bad").GetFile(context.Background(), "g/p", "config/a.yml", "dev")
	if err == nil || !strings.Contains(err.Error(), "GITLAB_TOKEN") {
		t.Fatalf("GetFile err = %v, want it to name the token", err)
	}
}

// The project path contains slashes and has to reach GitLab as %2F, or the
// request addresses a different endpoint entirely. Same for the file path.
func TestPathsAreEscapedForTheAPI(t *testing.T) {
	// GetFile makes its two requests at the same time, so the handler runs on
	// two goroutines and what they record has to be guarded.
	var mu sync.Mutex
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// RequestURI is the raw form, before Go decodes anything.
		mu.Lock()
		paths = append(paths, r.RequestURI)
		mu.Unlock()
		body, _ := json.Marshal(map[string]string{
			"content":        base64.StdEncoding.EncodeToString([]byte("k: v\n")),
			"encoding":       "base64",
			"last_commit_id": "abc123",
		})
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	_, err := New(srv.URL, "t").GetFile(context.Background(),
		"group/subgroup/configuration", "config/payments/application.yml", "main")
	if err != nil {
		t.Fatalf("GetFile: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(paths) == 0 {
		t.Fatal("GitLab was not called at all")
	}
	for _, p := range paths {
		if strings.Contains(strings.SplitN(p, "?", 2)[0], "group/subgroup") {
			t.Errorf("the project path reached GitLab with real slashes: %s", p)
		}
		if !strings.Contains(p, "%2F") {
			t.Errorf("nothing was escaped, so the URL addresses the wrong endpoint: %s", p)
		}
	}
}

// base64 is what GitLab actually sends; a client that returned it undecoded
// would put the encoded blob in the editor and commit it back verbatim.
func TestFileContentIsDecoded(t *testing.T) {
	const want = "spring:\n  datasource:\n    url: jdbc:oracle:thin:@host\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/commits") {
			_, _ = w.Write([]byte(`[{"author_name":"Ada","committed_date":"2026-08-12T10:00:00Z"}]`))
			return
		}
		body, _ := json.Marshal(map[string]string{
			"content":        base64.StdEncoding.EncodeToString([]byte(want)),
			"encoding":       "base64",
			"last_commit_id": "deadbeef",
		})
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	fc, err := New(srv.URL, "t").GetFile(context.Background(), "g/p", "config/a.yml", "dev")
	if err != nil {
		t.Fatalf("GetFile: %v", err)
	}
	if fc.Content != want {
		t.Errorf("Content = %q, want %q", fc.Content, want)
	}
	if fc.LastCommitID != "deadbeef" {
		t.Errorf("LastCommitID = %q", fc.LastCommitID)
	}
	if fc.Author != "Ada" {
		t.Errorf("Author = %q, want the commit author", fc.Author)
	}
}

// The listing pages, and a short page ends it. Without the stop condition the
// client would keep asking to the page cap on every load.
func TestRecursiveListingStopsOnAShortPage(t *testing.T) {
	pages := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pages++
		q, _ := url.ParseQuery(r.URL.RawQuery)
		var entries []TreeEntry
		if q.Get("page") == "1" {
			for i := 0; i < 100; i++ {
				entries = append(entries, TreeEntry{Name: "f", Type: "blob", Path: "config/f"})
			}
		} else {
			entries = []TreeEntry{{Name: "last", Type: "blob", Path: "config/last"}}
		}
		body, _ := json.Marshal(entries)
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	all, err := New(srv.URL, "t").ListTreeRecursive(context.Background(), "g/p", "config", "dev")
	if err != nil {
		t.Fatalf("ListTreeRecursive: %v", err)
	}
	if pages != 2 {
		t.Errorf("requested %d pages, want 2", pages)
	}
	if len(all) != 101 {
		t.Errorf("got %d entries, want 101", len(all))
	}
}
