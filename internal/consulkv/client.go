// Package consulkv reads and writes the service configuration kept in Consul's
// key/value store.
//
// It is deliberately small: list what is there, read one entry, write one
// entry. Which of those a given person may do is decided before anything here
// is called — see the access model.
package consulkv

import (
	"fmt"
	"sort"
	"strings"

	consulapi "github.com/hashicorp/consul/api"
)

// Client talks to one Consul, under one prefix. Nothing outside that prefix is
// reachable through it, which matches the token it is expected to carry.
type Client struct {
	kv     *consulapi.KV
	prefix string
	addr   string
}

// Entry is one configuration key.
type Entry struct {
	// Path is relative to the prefix: "abs/application.yml", not
	// "config/abs/application.yml". The prefix is deployment detail and does
	// not belong in the access model or on screen.
	Path string `json:"path"`
	// ModifyIndex is Consul's version of the entry. It travels to the browser
	// and back so that saving can refuse to overwrite someone else's change —
	// see Put.
	ModifyIndex uint64 `json:"modifyIndex"`
}

// New connects to Consul. The token comes from the environment
// (CONSUL_HTTP_TOKEN), as the Consul tooling expects.
func New(address, prefix string) (*Client, error) {
	cfg := consulapi.DefaultConfig()
	if address != "" {
		cfg.Address = address
	}
	c, err := consulapi.NewClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("consul: %w", err)
	}
	return &Client{kv: c.KV(), prefix: normalisePrefix(prefix), addr: cfg.Address}, nil
}

// Address reports where this client points, for logging.
func (c *Client) Address() string { return c.addr }

// Prefix reports the prefix every path is relative to. The browser is told, so
// that an empty page can say which prefix came back empty, and so the role
// editor can say what a path is expected to look like — writing the prefix into
// a grant by hand is the obvious mistake to make, and it fails silently.
func (c *Client) Prefix() string { return c.prefix }

// List returns every entry under the prefix, ordered by path.
func (c *Client) List() ([]Entry, error) {
	pairs, _, err := c.kv.List(c.prefix, nil)
	if err != nil {
		return nil, fmt.Errorf("consul: list %s: %w", c.prefix, err)
	}
	out := make([]Entry, 0, len(pairs))
	for _, p := range pairs {
		rel := strings.TrimPrefix(p.Key, c.prefix)
		// Consul represents a folder as an empty key ending in a slash. It is
		// not a configuration entry and has nothing to show.
		if rel == "" || strings.HasSuffix(rel, "/") {
			continue
		}
		out = append(out, Entry{Path: rel, ModifyIndex: p.ModifyIndex})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// Get returns one entry's content along with the version it was read at.
func (c *Client) Get(path string) (content string, modifyIndex uint64, err error) {
	pair, _, err := c.kv.Get(c.prefix+path, nil)
	if err != nil {
		return "", 0, fmt.Errorf("consul: get %s: %w", path, err)
	}
	if pair == nil {
		return "", 0, ErrNotFound
	}
	return string(pair.Value), pair.ModifyIndex, nil
}

// ErrNotFound is returned for a key that is not there.
var ErrNotFound = fmt.Errorf("no such configuration entry")

// ErrConflict is returned when the entry changed since it was read.
var ErrConflict = fmt.Errorf("the entry was changed by someone else since you opened it")

// Put writes an entry, but only if it still looks as it did when it was read.
//
// The check-and-set is the point. Two people editing the same file a minute
// apart would otherwise both succeed, and the first change would vanish with
// nothing to show that it ever existed. modifyIndex of 0 means the entry is
// expected not to exist yet, which is how a new one is created.
func (c *Client) Put(path, content string, modifyIndex uint64) error {
	ok, _, err := c.kv.CAS(&consulapi.KVPair{
		Key:         c.prefix + path,
		Value:       []byte(content),
		ModifyIndex: modifyIndex,
	}, nil)
	if err != nil {
		return fmt.Errorf("consul: put %s: %w", path, err)
	}
	if !ok {
		return ErrConflict
	}
	return nil
}

// PutUnchecked writes without the check-and-set.
//
// Only for the secondary side of a paired deployment, where a repository is the
// source of truth and Consul is its reflection. Checking there would be
// pretending Consul holds something worth protecting: the synchroniser
// overwrites it unconditionally too, and a conflict raised here would abort
// half way through, after the commit had already landed.
func (c *Client) PutUnchecked(path, content string) error {
	_, err := c.kv.Put(&consulapi.KVPair{Key: c.prefix + path, Value: []byte(content)}, nil)
	if err != nil {
		return fmt.Errorf("consul: put %s: %w", path, err)
	}
	return nil
}

// normalisePrefix makes the prefix end in exactly one slash, so that joining it
// to a path never produces "config//abs" or "configabs".
func normalisePrefix(p string) string {
	p = strings.Trim(p, "/")
	if p == "" {
		return ""
	}
	return p + "/"
}
