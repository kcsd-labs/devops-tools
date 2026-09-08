// Package store persists the part of the access model that is edited while the
// service runs, rather than deployed with it: who has signed in, and which
// roles they hold.
//
// It is a single JSON file, read into memory at startup and rewritten on
// change. The data is small — people and roles, not events — so a file is
// enough, and it stays readable, diffable and trivial to back up with
// `kubectl cp`. It lives on a volume; see the chart's persistence values.
package store

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"devops-tools/internal/config"
)

// User is someone who has signed in at least once.
//
// Groups come from the directory and are recorded for context only — they are
// what an administrator looks at when deciding what to grant. Roles are the
// grant itself, and are assigned in the portal.
type User struct {
	Username  string    `json:"username"`
	Email     string    `json:"email,omitempty"`
	Provider  string    `json:"provider"`
	Groups    []string  `json:"groups,omitempty"`
	Roles     []string  `json:"roles,omitempty"`
	FirstSeen time.Time `json:"firstSeen"`
	LastSeen  time.Time `json:"lastSeen"`

	// PasswordHash is set only for an account created here, with the local
	// provider. Accounts that come from the configuration file, or from a
	// directory, authenticate elsewhere and carry nothing secret in this file.
	PasswordHash string `json:"passwordHash,omitempty"`
	// Managed marks an account this portal owns: it can have its password
	// changed here, and it can be deleted. Everything else is a record of
	// someone who signed in, and deleting that would only make them reappear.
	Managed bool `json:"managed,omitempty"`
}

// file is the on-disk shape. The version field is here so a later format change
// can be recognised rather than guessed at.
type file struct {
	Version int                        `json:"version"`
	Users   map[string]*User           `json:"users"`
	Roles   map[string]config.RoleSpec `json:"roles,omitempty"`
}

// Version 2 added roles, which used to live only in the mounted configuration.
const currentVersion = 2

// seenResolution is how much of a change in "last seen" is worth a disk write.
// Without it every request from a token-based provider would rewrite the file.
const seenResolution = 5 * time.Minute

// Store is the in-memory model plus the file behind it.
type Store struct {
	path string

	mu sync.RWMutex
	// Keyed by lowercased username: directories are rarely case-sensitive, and
	// the same person signing in as "Ivan" and "ivan" must be one record.
	users map[string]*User
	// Roles are keyed by their exact name, which is what a token or a grant
	// refers to. Empty until seeded — see SeedRoles.
	roles map[string]config.RoleSpec
}

// Open reads the store, creating an empty one if the file does not exist yet.
func Open(path string) (*Store, error) {
	s := &Store{path: path, users: map[string]*User{}, roles: map[string]config.RoleSpec{}}

	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		// First start. Create the file now rather than at the first login, so
		// that an unwritable volume is reported at startup and not hours later.
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, fmt.Errorf("create the directory for %s: %w", path, err)
		}
		if err := s.save(); err != nil {
			return nil, err
		}
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	var f file
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if f.Version > currentVersion {
		return nil, fmt.Errorf("%s was written by a newer version of DevOps Tools (format %d, this build understands %d)",
			path, f.Version, currentVersion)
	}
	for k, u := range f.Users {
		if u != nil {
			s.users[k] = u
		}
	}
	for k, r := range f.Roles {
		s.roles[k] = r
	}
	return s, nil
}

// Count returns how many users are on record. Used to decide whether a fresh
// installation still needs seeding.
func (s *Store) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.users)
}

// RolesFor returns the roles assigned to a user, or nil for someone unknown or
// not yet granted anything.
func (s *Store) RolesFor(username string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	u, ok := s.users[key(username)]
	if !ok {
		return nil
	}
	return append([]string(nil), u.Roles...)
}

// GroupsFor returns the groups last seen for a user.
func (s *Store) GroupsFor(username string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	u, ok := s.users[key(username)]
	if !ok {
		return nil
	}
	return append([]string(nil), u.Groups...)
}

// List returns every user, ordered by username.
func (s *Store) List() []User {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]User, 0, len(s.users))
	for _, u := range s.users {
		out = append(out, *u)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Username < out[j].Username })
	return out
}

// RecordSeen registers a successful authentication.
//
// A user met for the first time is created with initialRoles, which is how a
// fresh installation ends up with a working administrator without anyone
// having to grant one. On every later sighting only the identity is refreshed:
// roles already assigned are never overwritten by the provider.
func (s *Store) RecordSeen(username, email, provider string, groups, initialRoles []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := nowUTC()
	u, ok := s.users[key(username)]
	if !ok {
		s.users[key(username)] = &User{
			Username:  username,
			Email:     email,
			Provider:  provider,
			Groups:    groups,
			Roles:     initialRoles,
			FirstSeen: now,
			LastSeen:  now,
		}
		return s.save()
	}

	claimed := u.Invited()
	if claimed {
		// An invitation just came good. Said out loud because the grant was
		// made days ago by somebody who is not here now, and the moment it
		// takes effect is otherwise invisible — the record simply stops looking
		// like an invitation.
		slog.Info("an invitation was claimed",
			"user", username, "provider", provider, "roles", u.Roles,
			"invited", u.FirstSeen.Format(time.RFC3339))
	}

	// Only write when something an administrator would notice has changed, or
	// enough time has passed to make "last seen" worth updating. Token-based
	// providers reach this on every single request.
	//
	// A claim always counts: it is the one write that must not be skipped, or
	// the record stays an invitation and is claimed again on the next request.
	changed := claimed || u.Email != email || u.Provider != provider || !sameStrings(u.Groups, groups)
	u.Username, u.Email, u.Provider, u.Groups = username, email, provider, groups
	if !changed && now.Sub(u.LastSeen) < seenResolution {
		return nil
	}
	u.LastSeen = now
	return s.save()
}

// save writes the whole file. The caller holds the lock.
//
// The write goes to a temporary file and is renamed into place, so that a crash
// or a full volume leaves the previous version intact rather than a truncated
// one — losing the access model to a half-written file is not a recoverable
// kind of failure.
func (s *Store) save() error {
	f := file{Version: currentVersion, Users: s.users, Roles: s.roles}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return fmt.Errorf("encode the store: %w", err)
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("replace %s: %w", s.path, err)
	}
	return nil
}

func key(username string) string { return strings.ToLower(strings.TrimSpace(username)) }

func nowUTC() time.Time { return time.Now().UTC() }

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
