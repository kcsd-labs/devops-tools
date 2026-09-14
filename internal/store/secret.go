package store

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
)

// secretKey is the entry inside the Secret, named for what it holds: somebody
// reading `kubectl get secret -o yaml` should not have to guess.
const secretKey = "access.json"

// maxSecretBytes is what the API server accepts in one Secret. Checked here so
// that an installation which outgrows it is told plainly, and early, instead of
// meeting whatever the API server says about an oversized request.
const maxSecretBytes = 1 << 20

// watchRetry is how long to wait before re-establishing a dropped watch. API
// servers rotate connections as a matter of course, so this is the ordinary
// path, not an error path.
const watchRetry = 5 * time.Second

// packAbove is where the document starts being compressed.
//
// Not always: below this the object can be read with `kubectl get secret -o
// yaml | base64 -d`, which is worth keeping for the size it will have in almost
// every installation. Above it, readability is the lesser worry — the data
// compresses to about a twentieth, which is the difference between an
// installation that fits and one that does not.
const packAbove = 256 << 10

// warnAbove is the share of the ceiling worth saying out loud. Reaching the
// limit should be something an operator saw coming, not something a refused
// write tells them.
const warnAbove = 0.7

// secretBackend keeps the access model in one Kubernetes Secret.
//
// This is what lets more than one replica share a model: the API server
// serialises the writes, resourceVersion refuses a stale one, and a watch tells
// the other replicas as soon as it lands. No volume, and no new component —
// the service already reads and writes Secrets, that being one of its pages.
//
// A Secret rather than a ConfigMap because the model is not secret but it is
// not a notice board either: who holds platform-admin should not be in reach of
// anybody who can list ConfigMaps.
type secretBackend struct {
	client    kubernetes.Interface
	namespace string
	name      string

	mu sync.Mutex
	// stored is what the object occupied when last read or written — after
	// packing, so it is the number the ceiling applies to.
	stored int
}

// NewSecretBackend keeps the access model in the named Secret.
func NewSecretBackend(c kubernetes.Interface, namespace, name string) Backend {
	return &secretBackend{client: c, namespace: namespace, name: name}
}

func (b *secretBackend) Describe() string { return "secret " + b.namespace + "/" + b.name }

func (b *secretBackend) Load(ctx context.Context) ([]byte, string, error) {
	sec, err := b.client.CoreV1().Secrets(b.namespace).Get(ctx, b.name, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		return nil, "", fs.ErrNotExist
	case err != nil:
		return nil, "", fmt.Errorf("read %s: %w", b.Describe(), err)
	}
	data, ok := sec.Data[secretKey]
	if !ok {
		return nil, "", fmt.Errorf("%s exists but has no %q entry", b.Describe(), secretKey)
	}
	b.note(len(data))

	plain, err := unpack(data)
	if err != nil {
		return nil, "", fmt.Errorf("read %s: %w", b.Describe(), err)
	}
	return plain, sec.ResourceVersion, nil
}

// note remembers how much room the object takes, so that the screen showing it
// does not have to ask again.
func (b *secretBackend) note(n int) {
	b.mu.Lock()
	b.stored = n
	b.mu.Unlock()
}

// StoredBytes is what the object occupies, as last seen. Zero before anything
// has been read or written.
func (b *secretBackend) StoredBytes() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.stored
}

func (b *secretBackend) Save(ctx context.Context, data []byte, version string) (string, error) {
	// Packed first, because the ceiling applies to what is stored — checking
	// the plain document would refuse models that fit comfortably.
	body := pack(data)
	if len(body) > maxSecretBytes {
		return "", fmt.Errorf(
			"the access model is %d bytes stored and a Secret holds at most %d; "+
				"keep it in a file instead (storage.backend: file) or remove records that are no longer needed",
			len(body), maxSecretBytes)
	}
	if share := float64(len(body)) / float64(maxSecretBytes); share > warnAbove {
		slog.Warn("the access model is approaching what a Secret can hold",
			"object", b.Describe(), "bytes", len(body), "limit", maxSecretBytes,
			"share", fmt.Sprintf("%.0f%%", share*100))
	}
	sec := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      b.name,
			Namespace: b.namespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "devops-tools",
				"app.kubernetes.io/component":  "access-model",
			},
		},
		Data: map[string][]byte{secretKey: body},
	}

	secrets := b.client.CoreV1().Secrets(b.namespace)
	if version == "" {
		created, err := secrets.Create(ctx, sec, metav1.CreateOptions{})
		switch {
		case apierrors.IsAlreadyExists(err):
			// Another replica created it first. Same answer as a conflicting
			// update: reload and apply the change again.
			return "", fmt.Errorf("%s: %w", b.Describe(), ErrConflict)
		case err != nil:
			return "", fmt.Errorf("create %s: %w", b.Describe(), err)
		}
		b.note(len(body))
		return created.ResourceVersion, nil
	}

	sec.ResourceVersion = version
	updated, err := secrets.Update(ctx, sec, metav1.UpdateOptions{})
	switch {
	case apierrors.IsConflict(err):
		return "", fmt.Errorf("%s: %w", b.Describe(), ErrConflict)
	case apierrors.IsNotFound(err):
		// Deleted underneath us. Treat it as a conflict so the caller reloads,
		// finds nothing, and writes it back rather than failing outright.
		return "", fmt.Errorf("%s: %w", b.Describe(), ErrConflict)
	case err != nil:
		return "", fmt.Errorf("write %s: %w", b.Describe(), err)
	}
	b.note(len(body))
	return updated.ResourceVersion, nil
}

// Watch reports changes made elsewhere until ctx ends.
//
// A plain watch rather than an informer: an informer brings a cache and a work
// queue for a single object that is held in memory anyway.
func (b *secretBackend) Watch(ctx context.Context, onChange func(data []byte, version string)) {
	for ctx.Err() == nil {
		w, err := b.client.CoreV1().Secrets(b.namespace).Watch(ctx, metav1.ListOptions{
			FieldSelector: "metadata.name=" + b.name,
		})
		if err != nil {
			slog.Warn("cannot watch the access model; other replicas' changes will not arrive until this recovers",
				"object", b.Describe(), "error", err, "retry_in", watchRetry)
			b.pause(ctx)
			continue
		}
		for event := range w.ResultChan() {
			sec, ok := event.Object.(*corev1.Secret)
			if !ok {
				continue
			}
			switch event.Type {
			case watch.Added, watch.Modified:
				data, ok := sec.Data[secretKey]
				if !ok {
					continue
				}
				b.note(len(data))
				plain, err := unpack(data)
				if err != nil {
					slog.Error("a change to the access model could not be unpacked",
						"object", b.Describe(), "error", err)
					continue
				}
				onChange(plain, sec.ResourceVersion)
			case watch.Deleted:
				// Said loudly: the access model is gone from the cluster, and
				// this replica is now the only copy of it.
				slog.Error("the access model was deleted; this replica still holds it in memory "+
					"and will write it back on the next change",
					"object", b.Describe())
			}
		}
		w.Stop()
		// The channel closes when the API server rotates the connection, which
		// is routine. Re-establish rather than treating it as a failure.
		b.pause(ctx)
	}
}

func (b *secretBackend) pause(ctx context.Context) {
	select {
	case <-ctx.Done():
	case <-time.After(watchRetry):
	}
}

// Limit is what one Secret holds. Shown before it is reached, so that an
// installation which is outgrowing it finds out from a number on a screen
// rather than from a refused write.
func (b *secretBackend) Limit() int { return maxSecretBytes }

// pack compresses the document once it is large enough to be worth it.
//
// Self-describing: gzip's own signature says which it is, so nothing has to be
// recorded alongside, documents written before this existed keep opening, and a
// copy somebody compressed by hand is read too.
func pack(data []byte) []byte {
	if len(data) < packAbove {
		return data
	}
	var buf bytes.Buffer
	w, err := gzip.NewWriterLevel(&buf, gzip.BestSpeed)
	if err != nil {
		return data // not worth failing a write over
	}
	if _, err := w.Write(data); err != nil {
		return data
	}
	if err := w.Close(); err != nil {
		return data
	}
	return buf.Bytes()
}

// unpack reverses pack, and leaves a plain document alone.
func unpack(data []byte) ([]byte, error) {
	if !Compressed(data) {
		return data, nil
	}
	r, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("the stored model looks compressed but will not open: %w", err)
	}
	defer func() { _ = r.Close() }()

	// Bounded: what is read here comes from the cluster, and an object that
	// expands without end should fail rather than fill memory.
	out, err := io.ReadAll(io.LimitReader(r, maxUnpacked))
	if err != nil {
		return nil, fmt.Errorf("read the compressed model: %w", err)
	}
	return out, nil
}

// maxUnpacked bounds what a compressed document may expand to.
const maxUnpacked = 64 << 20

// Compressed reports whether a document is gzipped, by its signature.
func Compressed(data []byte) bool {
	return len(data) > 2 && data[0] == 0x1f && data[1] == 0x8b
}
