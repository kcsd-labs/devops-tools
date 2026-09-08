package configsource

import (
	"context"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"

	"devops-tools/internal/gitlab"
)

// Git serves the Configurations page from a GitLab repository.
//
// Where a synchroniser copies the repository into Consul, this is the end to
// edit: a change written straight to Consul is overwritten the next time the
// synchroniser runs, and leaves no trace of who made it or why.
type Git struct {
	cl       *gitlab.Client
	project  string
	branch   string
	basePath string // "" means the repository root
}

func NewGit(cl *gitlab.Client, project, branch, basePath string) *Git {
	return &Git{cl: cl, project: project, branch: branch, basePath: strings.Trim(basePath, "/")}
}

func (g *Git) Kind() string { return "git" }

func (g *Git) Root() string {
	if g.basePath == "" {
		return g.project + "@" + g.branch
	}
	return g.project + "@" + g.branch + ":" + g.basePath + "/"
}

// repoPath turns a path relative to basePath into one relative to the
// repository root. Everything above this package works in the former, so that
// a grant means the same thing whichever source is configured.
func (g *Git) repoPath(rel string) string {
	if g.basePath == "" {
		return rel
	}
	return g.basePath + "/" + rel
}

func (g *Git) List(ctx context.Context) ([]Entry, error) {
	entries, err := g.cl.ListTreeRecursive(ctx, g.project, g.basePath, g.branch)
	if err != nil {
		return nil, err
	}
	out := make([]Entry, 0, len(entries))
	for _, e := range entries {
		// Directories are structure, not configuration; the tree in the browser
		// is rebuilt from the paths anyway.
		if e.Type != "blob" {
			continue
		}
		rel := strings.TrimPrefix(e.Path, g.basePath)
		rel = strings.TrimPrefix(rel, "/")
		if rel == "" {
			continue
		}
		// Version is deliberately left empty in a listing. Filling it would mean
		// one API call per file to learn each blob's last commit, and the only
		// version that matters is the one read when the file is opened.
		out = append(out, Entry{Path: rel})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

func (g *Git) Get(ctx context.Context, rel string) (File, error) {
	fc, err := g.cl.GetFile(ctx, g.project, g.repoPath(rel), g.branch)
	if errors.Is(err, gitlab.ErrNotFound) {
		return File{}, ErrNotFound
	}
	if err != nil {
		return File{}, err
	}
	return File{
		Entry:     Entry{Path: rel, Version: fc.LastCommitID},
		Content:   fc.Content,
		Author:    fc.Author,
		UpdatedAt: fc.UpdatedAt,
	}, nil
}

func (g *Git) Put(ctx context.Context, rel, content, version string, by Editor) error {
	err := g.cl.PutFile(ctx, g.project, g.repoPath(rel), g.branch, content, gitlab.Commit{
		Message: commitMessage(rel, by),
		// The person in the portal, not the token's owner. Otherwise every
		// change in the repository's history is attributed to a service
		// account, and the history stops answering the question it is for.
		AuthorName:   by.Username,
		AuthorEmail:  by.Email,
		LastCommitID: version,
	})
	if errors.Is(err, gitlab.ErrConflict) {
		return ErrConflict
	}
	if errors.Is(err, gitlab.ErrNotFound) {
		return ErrNotFound
	}
	return err
}

// commitMessage names the file and who changed it. Whoever runs `git log` on
// this repository is usually asking when a setting changed and on whose
// authority, and the portal is the only place that knows the second part.
func commitMessage(rel string, by Editor) string {
	who := by.Username
	if who == "" {
		who = "an unnamed user"
	}
	return fmt.Sprintf("Update %s\n\nEdited in DevOps Tools by %s.", path.Base(rel), who)
}
