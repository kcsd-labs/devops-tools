// Package gitlab is a small client for the parts of the GitLab API this portal
// needs: reading a repository's tree and files, and committing a change back.
//
// It is not a general GitLab library. Everything here exists to serve the
// Configurations page, where the repository is the source of truth and a
// synchroniser copies it onward into Consul.
package gitlab

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ErrConflict means the file changed since it was opened. Saving anyway would
// discard somebody else's commit, so the save is refused and the browser is
// told to compare before overwriting.
var ErrConflict = errors.New("the file was changed in GitLab since you opened it")

// ErrNotFound is a 404 from GitLab: no such project, branch, path or file.
var ErrNotFound = errors.New("not found in GitLab")

type Client struct {
	base  string
	token string
	http  *http.Client
}

func New(base, token string) *Client {
	return &Client{
		base:  strings.TrimRight(base, "/"),
		token: token,
		http:  &http.Client{Timeout: 20 * time.Second},
	}
}

// Base reports the GitLab this client talks to, for logging.
func (c *Client) Base() string { return c.base }

// TreeEntry is one node of the repository tree.
type TreeEntry struct {
	Name string `json:"name"`
	Type string `json:"type"` // tree | blob
	Path string `json:"path"`
}

// maxTreePages bounds a recursive listing at 5000 entries. A configuration
// repository does not reach that; a runaway request against the wrong project
// would, and paging it forever helps nobody.
const maxTreePages = 50

// ListTreeRecursive returns every entry under path on branch ref.
func (c *Client) ListTreeRecursive(ctx context.Context, projectPath, path, ref string) ([]TreeEntry, error) {
	proj := url.PathEscape(projectPath) // the slashes become %2F, as GitLab requires
	var all []TreeEntry
	for page := 1; page <= maxTreePages; page++ {
		u := fmt.Sprintf("%s/api/v4/projects/%s/repository/tree?ref=%s&recursive=true&per_page=100&page=%d",
			c.base, proj, url.QueryEscape(ref), page)
		if path != "" {
			u += "&path=" + url.QueryEscape(path)
		}
		var entries []TreeEntry
		if err := c.getJSON(ctx, u, &entries); err != nil {
			return nil, err
		}
		all = append(all, entries...)
		if len(entries) < 100 {
			break
		}
	}
	return all, nil
}

// FileContent is a file with the commit information needed to show who last
// touched it and to refuse a save that would overwrite them.
type FileContent struct {
	Path         string
	Content      string
	LastCommitID string
	Author       string
	UpdatedAt    string
}

// GetFile reads a file from branch ref, along with who last changed it.
//
// Two requests, because GitLab does not return the author with the file. They
// are made at the same time rather than one after the other: the second is only
// there to fill in a line of prose, and nobody should wait a whole extra round
// trip for it.
func (c *Client) GetFile(ctx context.Context, projectPath, filePath, ref string) (FileContent, error) {
	proj := url.PathEscape(projectPath)
	fp := url.PathEscape(filePath)

	type commitInfo struct{ author, at string }
	commits := make(chan commitInfo, 1)
	go func() {
		var out []struct {
			AuthorName    string `json:"author_name"`
			CommittedDate string `json:"committed_date"`
		}
		u := fmt.Sprintf("%s/api/v4/projects/%s/repository/commits?path=%s&ref_name=%s&per_page=1",
			c.base, proj, url.QueryEscape(filePath), url.QueryEscape(ref))
		// Best effort: failing to find out who touched it last is not a reason
		// to refuse to show the file.
		if err := c.getJSON(ctx, u, &out); err == nil && len(out) > 0 {
			commits <- commitInfo{out[0].AuthorName, out[0].CommittedDate}
			return
		}
		commits <- commitInfo{}
	}()

	var raw struct {
		Content      string `json:"content"`
		Encoding     string `json:"encoding"`
		LastCommitID string `json:"last_commit_id"`
	}
	u := fmt.Sprintf("%s/api/v4/projects/%s/repository/files/%s?ref=%s",
		c.base, proj, fp, url.QueryEscape(ref))
	err := c.getJSON(ctx, u, &raw)

	info := <-commits // always drain, so the goroutine never leaks
	if err != nil {
		return FileContent{}, err
	}
	data := raw.Content
	if raw.Encoding == "base64" {
		b, decErr := base64.StdEncoding.DecodeString(raw.Content)
		if decErr != nil {
			return FileContent{}, fmt.Errorf("gitlab: could not decode %s: %w", filePath, decErr)
		}
		data = string(b)
	}
	return FileContent{
		Path:         filePath,
		Content:      data,
		LastCommitID: raw.LastCommitID,
		Author:       info.author,
		UpdatedAt:    info.at,
	}, nil
}

// Commit describes the change being made. The author is the person in the
// portal rather than the token's owner, so the repository history says who
// actually made the edit instead of attributing everything to a service
// account.
type Commit struct {
	Message     string
	AuthorName  string
	AuthorEmail string
	// LastCommitID is the commit the editor opened the file at. GitLab refuses
	// the write if the file has moved on, which is what turns a silent
	// overwrite into ErrConflict.
	LastCommitID string
}

// PutFile commits a change to a single file on branch ref.
func (c *Client) PutFile(ctx context.Context, projectPath, filePath, ref, content string, commit Commit) error {
	proj := url.PathEscape(projectPath)
	fp := url.PathEscape(filePath)
	u := fmt.Sprintf("%s/api/v4/projects/%s/repository/files/%s", c.base, proj, fp)

	body := map[string]string{"branch": ref, "content": content, "commit_message": commit.Message}
	if commit.AuthorName != "" {
		body["author_name"] = commit.AuthorName
	}
	if commit.AuthorEmail != "" {
		body["author_email"] = commit.AuthorEmail
	}
	if commit.LastCommitID != "" {
		body["last_commit_id"] = commit.LastCommitID
	}
	buf, _ := json.Marshal(body)

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, u, bytes.NewReader(buf))
	if err != nil {
		return err
	}
	req.Header.Set("PRIVATE-TOKEN", c.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 == 2 {
		return nil
	}
	b, _ := io.ReadAll(resp.Body)
	msg := strings.TrimSpace(string(b))
	if isConflict(resp.StatusCode, msg) {
		return ErrConflict
	}
	if resp.StatusCode == http.StatusNotFound {
		return ErrNotFound
	}
	return fmt.Errorf("gitlab returned %d: %s", resp.StatusCode, msg)
}

// isConflict recognises a stale last_commit_id.
//
// GitLab does not use 409 for this. It answers 400 with a message saying the
// file changed, so the text has to be matched — and because that is fragile,
// the status alone is never treated as success anywhere.
func isConflict(status int, msg string) bool {
	if status == http.StatusConflict {
		return true
	}
	return status == http.StatusBadRequest && strings.Contains(msg, "changed since")
}

func (c *Client) getJSON(ctx context.Context, u string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("PRIVATE-TOKEN", c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return ErrNotFound
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		// Worth separating from any other failure: this one is always the token,
		// and saying so saves an hour of looking at the repository instead.
		return fmt.Errorf("gitlab refused the token (%d) — check GITLAB_TOKEN and its scope", resp.StatusCode)
	}
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("gitlab returned status %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
