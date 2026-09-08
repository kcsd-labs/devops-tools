package config

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	consulapi "github.com/hashicorp/consul/api"
)

// Loader reads raw configuration bytes by key ("application.yaml").
type Loader interface {
	Read(key string) ([]byte, error)
	// Source describes where the configuration came from, for logging.
	Source() string
}

// Watcher is a Loader that can notify on changes, enabling hot reload of the
// access model. Implemented by Consul; the file loader does not implement it.
type Watcher interface {
	// Watch blocks until the context is cancelled, invoking onChange on every
	// update of the key.
	Watch(ctx context.Context, key string, onChange func([]byte))
}

// --- File source: the default, backed by a mounted ConfigMap ---

// FileLoader reads keys as files inside a directory.
type FileLoader struct {
	dir string
}

func NewFileLoader(dir string) *FileLoader {
	return &FileLoader{dir: dir}
}

func (l *FileLoader) Read(key string) ([]byte, error) {
	return os.ReadFile(filepath.Join(l.dir, key))
}

func (l *FileLoader) Source() string {
	return "file:" + l.dir
}

// --- Consul KV source: optional, for deployments already standardised on it ---

// ConsulLoader reads keys from Consul KV under prefix/<key>.
type ConsulLoader struct {
	kv     *consulapi.KV
	prefix string
	addr   string
}

// NewConsulLoader builds the Consul client. The address and token also honour
// the standard CONSUL_HTTP_ADDR and CONSUL_HTTP_TOKEN variables.
func NewConsulLoader(addr, prefix string) (*ConsulLoader, error) {
	cfg := consulapi.DefaultConfig()
	if addr != "" {
		cfg.Address = addr
	}
	client, err := consulapi.NewClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("consul client: %w", err)
	}
	return &ConsulLoader{kv: client.KV(), prefix: prefix, addr: cfg.Address}, nil
}

func (l *ConsulLoader) Read(key string) ([]byte, error) {
	path := l.prefix + "/" + key
	pair, _, err := l.kv.Get(path, nil)
	if err != nil {
		return nil, fmt.Errorf("consul get %q: %w", path, err)
	}
	if pair == nil {
		return nil, fmt.Errorf("consul key %q not found", path)
	}
	return pair.Value, nil
}

func (l *ConsulLoader) Source() string {
	return fmt.Sprintf("consul:%s/%s", l.addr, l.prefix)
}

// Watch uses Consul blocking queries: the request hangs until the key changes
// or the wait time elapses.
func (l *ConsulLoader) Watch(ctx context.Context, key string, onChange func([]byte)) {
	path := l.prefix + "/" + key
	var lastIndex uint64
	for {
		if ctx.Err() != nil {
			return
		}
		// Keep the wait short: a proxy in front of Consul will usually cut a
		// long-lived blocking query with a 504. Reaction time is unaffected —
		// a change returns the query immediately.
		pair, meta, err := l.kv.Get(path, (&consulapi.QueryOptions{
			WaitIndex: lastIndex,
			WaitTime:  30 * time.Second,
		}).WithContext(ctx))
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			slog.Warn("consul watch error", "key", path, "err", err)
			time.Sleep(5 * time.Second)
			continue
		}
		// Consul recommends resetting the index if it goes backwards.
		if meta.LastIndex < lastIndex {
			lastIndex = 0
			continue
		}
		if meta.LastIndex == lastIndex {
			continue // blocking query timed out with no change
		}
		lastIndex = meta.LastIndex
		if pair != nil {
			onChange(pair.Value)
		}
	}
}

// LoaderFromEnv picks the configuration source:
//
//	DEVOPS_TOOLS_CONFIG_SOURCE=file    (default) — DEVOPS_TOOLS_CONFIG_DIR, default ./config
//	DEVOPS_TOOLS_CONFIG_SOURCE=consul            — DEVOPS_TOOLS_CONSUL_ADDR, DEVOPS_TOOLS_CONSUL_PREFIX
func LoaderFromEnv() (Loader, error) {
	switch os.Getenv("DEVOPS_TOOLS_CONFIG_SOURCE") {
	case "consul":
		prefix := envOr("DEVOPS_TOOLS_CONSUL_PREFIX", "config/devops-tools")
		return NewConsulLoader(os.Getenv("DEVOPS_TOOLS_CONSUL_ADDR"), prefix)
	default:
		return NewFileLoader(envOr("DEVOPS_TOOLS_CONFIG_DIR", "./config")), nil
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
