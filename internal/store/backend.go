package store

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// Backend is where the access model is kept between restarts.
//
// The interface exists for a second implementation — a Kubernetes object, which
// is what lets more than one replica share one model — so that it arrives
// without touching the ten methods that change the model.
type Backend interface {
	// Load returns the stored document and the version it was read at. A store
	// that does not exist yet returns fs.ErrNotExist, so the caller can tell
	// "first start" from "unreadable".
	Load(ctx context.Context) (data []byte, version string, err error)

	// Save writes the document if the stored version is still the one given.
	// An empty version means "this should not exist yet". A version that has
	// moved on returns ErrConflict rather than overwriting.
	Save(ctx context.Context, data []byte, version string) (string, error)

	// Watch reports changes made elsewhere until ctx ends. A backend with a
	// single writer has nothing to report and may do nothing.
	Watch(ctx context.Context, onChange func(data []byte, version string))

	// Describe names the backend for startup logs and error messages.
	Describe() string
}

// ErrConflict says somebody else wrote since this copy was read. The caller
// reloads and applies its change again — writing the stale document back would
// silently undo whatever the other writer just did.
var ErrConflict = errors.New("the access model was written by somebody else")

// fileBackend keeps the model in one JSON file.
//
// This is the original arrangement, and remains the one for running outside
// Kubernetes. It supports a single writer: the version check below catches an
// edit made by hand or by a second process, but a file on a ReadWriteOnce
// volume cannot be shared by two replicas in the first place.
type fileBackend struct{ path string }

// NewFileBackend keeps the access model in the file at path.
func NewFileBackend(path string) Backend { return &fileBackend{path: path} }

func (b *fileBackend) Describe() string { return "file " + b.path }

// Watch does nothing. A file on a ReadWriteOnce volume has one writer, which is
// the whole reason this backend cannot serve more than one replica.
func (b *fileBackend) Watch(context.Context, func([]byte, string)) {}

func (b *fileBackend) Load(_ context.Context) ([]byte, string, error) {
	data, err := os.ReadFile(b.path)
	if err != nil {
		return nil, "", err // including fs.ErrNotExist, which the caller reads
	}
	return data, b.version(), nil
}

func (b *fileBackend) Save(_ context.Context, data []byte, version string) (string, error) {
	if current := b.version(); current != version {
		return "", fmt.Errorf("%s: %w", b.path, ErrConflict)
	}
	if err := os.MkdirAll(filepath.Dir(b.path), 0o755); err != nil {
		return "", fmt.Errorf("create the directory for %s: %w", b.path, err)
	}
	// Written to a temporary file and renamed into place, so that a crash or a
	// full volume leaves the previous version intact rather than a truncated
	// one — losing the access model to a half-written file is not a recoverable
	// kind of failure.
	tmp := b.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return "", fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, b.path); err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("replace %s: %w", b.path, err)
	}
	return b.version(), nil
}

// version is what the file looked like when last seen: its size and the moment
// it was written. Not a checksum — reading the whole file to decide whether to
// write it would cost more than the conflict it guards against is worth.
func (b *fileBackend) version() string {
	fi, err := os.Stat(b.path)
	if err != nil {
		return "" // absent, which is the version a first write expects
	}
	return fmt.Sprintf("%d-%d", fi.Size(), fi.ModTime().UnixNano())
}

// MigrateFile copies a file store into a backend that has none yet.
//
// It runs once: after the first write the backend holds the model and the file
// is left alone. Never deleted — an upgrade that turns out badly is undone by
// pointing back at the file, and that only works if it is still there.
func MigrateFile(ctx context.Context, path string, b Backend) (bool, error) {
	switch _, _, err := b.Load(ctx); {
	case err == nil:
		return false, nil // already has a model; nothing to do
	case !errors.Is(err, fs.ErrNotExist):
		return false, fmt.Errorf("check %s before migrating: %w", b.Describe(), err)
	}

	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return false, nil // a fresh installation, with nothing to carry over
	case err != nil:
		return false, fmt.Errorf("read %s: %w", path, err)
	}
	if _, err := b.Save(ctx, data, ""); err != nil {
		return false, fmt.Errorf("copy %s into %s: %w", path, b.Describe(), err)
	}
	return true, nil
}
