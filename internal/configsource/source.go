// Package configsource is where the Configurations page gets its files from.
//
// Two sources exist: Consul's key/value store, and a Git repository that a
// synchroniser copies into Consul. They are different enough underneath —
// one versions by an integer, the other by a commit hash — that the page would
// otherwise have to know which it is talking to, and every feature added later
// would have to be written twice.
package configsource

import (
	"context"
	"errors"
)

// ErrNotFound is returned for a path that does not exist.
var ErrNotFound = errors.New("no such configuration entry")

// ErrConflict is returned when the entry changed since it was read. Every
// source must be able to report this: a save that silently discards somebody
// else's change is the one failure this package exists to prevent.
var ErrConflict = errors.New("the entry was changed by someone else since you opened it")

// Entry is one configuration file, as listed.
type Entry struct {
	// Path is relative to the source's root, so that a grant written against
	// one source keeps its meaning against the other. Consul's prefix and the
	// repository's base path are deployment detail and never appear here.
	Path string `json:"path"`
	// Version identifies what was read. It is opaque: Consul's ModifyIndex and
	// GitLab's last commit id are both just strings to everything above this
	// package, which is what lets one page serve both.
	Version string `json:"version"`
}

// File is an entry with its content.
type File struct {
	Entry
	Content string `json:"content"`
	// Author and UpdatedAt are best effort — Consul does not record who wrote a
	// key, so they are empty there rather than invented.
	Author    string `json:"author,omitempty"`
	UpdatedAt string `json:"updatedAt,omitempty"`
}

// Editor is who is making the change, for whatever record the source keeps.
type Editor struct {
	Username string
	Email    string
}

// Source is one place configuration lives.
type Source interface {
	// Kind is shown in the interface so it is never a mystery which system a
	// change just went to.
	Kind() string
	// Root is what paths are relative to, for messages and for the role editor.
	Root() string
	List(ctx context.Context) ([]Entry, error)
	Get(ctx context.Context, path string) (File, error)
	// Put writes, but only if the entry still matches version. An empty version
	// means the entry is expected not to exist yet.
	Put(ctx context.Context, path, content, version string, by Editor) error
}
