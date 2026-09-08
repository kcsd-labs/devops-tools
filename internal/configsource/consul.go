package configsource

import (
	"context"
	"errors"
	"strconv"

	"devops-tools/internal/consulkv"
)

// Consul serves the Configurations page straight from the key/value store.
//
// Nothing here records who made a change: Consul has no notion of an author.
// The audit log does, which is why the portal writes one on every save.
type Consul struct{ kv *consulkv.Client }

func NewConsul(kv *consulkv.Client) *Consul { return &Consul{kv: kv} }

func (c *Consul) Kind() string { return "consul" }
func (c *Consul) Root() string { return c.kv.Prefix() }

func (c *Consul) List(_ context.Context) ([]Entry, error) {
	entries, err := c.kv.List()
	if err != nil {
		return nil, err
	}
	out := make([]Entry, 0, len(entries))
	for _, e := range entries {
		out = append(out, Entry{Path: e.Path, Version: strconv.FormatUint(e.ModifyIndex, 10)})
	}
	return out, nil
}

func (c *Consul) Get(_ context.Context, path string) (File, error) {
	content, index, err := c.kv.Get(path)
	if errors.Is(err, consulkv.ErrNotFound) {
		return File{}, ErrNotFound
	}
	if err != nil {
		return File{}, err
	}
	return File{
		Entry:   Entry{Path: path, Version: strconv.FormatUint(index, 10)},
		Content: content,
	}, nil
}

// PutUnchecked writes without comparing versions. See consulkv.PutUnchecked for
// why the secondary side of a pair is written this way.
func (c *Consul) PutUnchecked(_ context.Context, path, content string) error {
	return c.kv.PutUnchecked(path, content)
}

func (c *Consul) Put(_ context.Context, path, content, version string, _ Editor) error {
	// A version that will not parse is treated as 0, which check-and-set reads
	// as "must not exist yet" and refuses for anything that does. Guessing an
	// index instead would be guessing which change to overwrite.
	index, _ := strconv.ParseUint(version, 10, 64)
	err := c.kv.Put(path, content, index)
	if errors.Is(err, consulkv.ErrConflict) {
		return ErrConflict
	}
	return err
}
