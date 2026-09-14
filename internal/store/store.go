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
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
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

// file is the stored shape. The version field is here so a later format change
// can be recognised rather than guessed at.
type file struct {
	Version int `json:"version"`
	// Groups is the table version 3 introduced. See storedUser.
	Groups []string                   `json:"groups,omitempty"`
	Users  map[string]*storedUser     `json:"users"`
	Roles  map[string]config.RoleSpec `json:"roles,omitempty"`
	// Lockouts are accounts shut out after too many failed attempts. Stored so
	// that every replica honours one, and pruned as they elapse.
	Lockouts map[string]time.Time `json:"lockouts,omitempty"`
}

// storedUser is a User as written down. It differs in one place: the groups.
//
// A directory user carries dozens of full DNs, and in an organisation they are
// the same few hundred DNs over and over — the same team written out once per
// member. Version 3 keeps the distinct names in one table and gives each user
// indices into it, which is about ten times smaller on a real directory. That
// matters once the model lives in a Secret, where a megabyte is the ceiling.
type storedUser struct {
	Username string `json:"username"`
	Email    string `json:"email,omitempty"`
	Provider string `json:"provider"`
	// Groups is how version 2 wrote them: the names themselves. Still read, so
	// that a store written by an older build opens.
	Groups []string `json:"groups,omitempty"`
	// GroupIDs is version 3: indices into file.Groups.
	GroupIDs     []int     `json:"groupIds,omitempty"`
	Roles        []string  `json:"roles,omitempty"`
	FirstSeen    time.Time `json:"firstSeen"`
	LastSeen     time.Time `json:"lastSeen"`
	PasswordHash string    `json:"passwordHash,omitempty"`
	Managed      bool      `json:"managed,omitempty"`
}

// Version 2 added roles, which used to live only in the mounted configuration.
// Version 3 moved group names into a table.
const currentVersion = 3

// expandGroups reads a stored user's groups, in either format.
func expandGroups(u *storedUser, table []string) []string {
	if len(u.GroupIDs) == 0 {
		return u.Groups // version 2, or somebody with no groups at all
	}
	out := make([]string, 0, len(u.GroupIDs))
	for _, i := range u.GroupIDs {
		if i >= 0 && i < len(table) {
			out = append(out, table[i])
		}
		// An index outside the table means a damaged document. Dropping the
		// name is better than refusing to open: groups are context for an
		// administrator, and losing one must not lock everybody out.
	}
	return out
}

// encodeFile builds the stored shape, gathering the distinct group names into
// one table. Sorted, so that an unchanged model encodes to the same bytes and
// does not look like a change to whoever is watching the object.
func encodeFile(users map[string]*User, roles map[string]config.RoleSpec, lockouts map[string]time.Time) file {
	index := map[string]int{}
	var table []string
	for _, u := range users {
		for _, g := range u.Groups {
			if _, ok := index[g]; !ok {
				index[g] = 0
				table = append(table, g)
			}
		}
	}
	sort.Strings(table)
	for i, g := range table {
		index[g] = i
	}

	stored := make(map[string]*storedUser, len(users))
	for k, u := range users {
		su := &storedUser{
			Username: u.Username, Email: u.Email, Provider: u.Provider,
			Roles: u.Roles, FirstSeen: u.FirstSeen, LastSeen: u.LastSeen,
			PasswordHash: u.PasswordHash, Managed: u.Managed,
		}
		for _, g := range u.Groups {
			su.GroupIDs = append(su.GroupIDs, index[g])
		}
		stored[k] = su
	}
	return file{Version: currentVersion, Groups: table, Users: stored, Roles: roles, Lockouts: lockouts}
}

// seenFlush is how often the accumulated "last seen" times are written.
//
// They are batched because a token-based provider records a sighting on every
// single request. One write for everybody every few minutes is the cost; one
// write per active person was what it used to be, and the column is not read to
// the second by anyone.
const seenFlush = 5 * time.Minute

// Store is the in-memory model plus the file behind it.
type Store struct {
	backend Backend
	// version is what the backend held when this copy was read. Sent back with
	// every write so that a change made elsewhere is refused rather than
	// overwritten.
	version string

	mu sync.RWMutex
	// seenDirty marks last-seen times that are in memory but not yet written.
	seenDirty bool
	// lastWrite is when this replica last wrote. Zero if it has not, which is
	// the ordinary state of a replica that has only been reading.
	lastWrite time.Time
	// Keyed by lowercased username: directories are rarely case-sensitive, and
	// the same person signing in as "Ivan" and "ivan" must be one record.
	users map[string]*User
	// Roles are keyed by their exact name, which is what a token or a grant
	// refers to. Empty until seeded — see SeedRoles.
	roles map[string]config.RoleSpec
	// lockouts are accounts shut out after too many failed password attempts,
	// keyed like users. Shared so that the allowance does not multiply by the
	// number of replicas — see LockUntil.
	lockouts map[string]time.Time
}

// Open reads the store from a file, creating an empty one if it does not exist.
func Open(path string) (*Store, error) { return OpenWith(NewFileBackend(path)) }

// OpenWith reads the store from any backend.
func OpenWith(b Backend) (*Store, error) {
	s := &Store{backend: b, users: map[string]*User{}, roles: map[string]config.RoleSpec{},
		lockouts: map[string]time.Time{}}
	err := s.reload()
	switch {
	case err == nil:
		return s, nil
	case !errors.Is(err, fs.ErrNotExist):
		return nil, err
	}
	// First start. Write the empty store now rather than at the first login, so
	// that somewhere unwritable is reported at startup and not hours later.
	if err := s.persist(); err != nil {
		return nil, err
	}
	return s, nil
}

// reload replaces the in-memory model with what the backend holds. The caller
// holds the lock, except at startup where there is nobody to hold it against.
func (s *Store) reload() error {
	data, version, err := s.backend.Load(context.Background())
	if err != nil {
		return err
	}
	return s.apply(data, version)
}

// apply replaces the in-memory model with a document read from the backend.
// The caller holds the lock.
func (s *Store) apply(data []byte, version string) error {
	var f file
	if err := json.Unmarshal(data, &f); err != nil {
		return fmt.Errorf("parse the access model from %s: %w", s.backend.Describe(), err)
	}
	if f.Version > currentVersion {
		return fmt.Errorf("%s was written by a newer version of DevOps Tools (format %d, this build understands %d)",
			s.backend.Describe(), f.Version, currentVersion)
	}
	s.set(f)
	s.version = version
	return nil
}

// set replaces the in-memory model with a parsed document. The caller holds the
// lock.
func (s *Store) set(f file) {
	users := make(map[string]*User, len(f.Users))
	for k, u := range f.Users {
		if u == nil {
			continue
		}
		users[k] = &User{
			Username: u.Username, Email: u.Email, Provider: u.Provider,
			Groups: expandGroups(u, f.Groups), Roles: u.Roles,
			FirstSeen: u.FirstSeen, LastSeen: u.LastSeen,
			PasswordHash: u.PasswordHash, Managed: u.Managed,
		}
	}
	roles := make(map[string]config.RoleSpec, len(f.Roles))
	for k, r := range f.Roles {
		roles[k] = r
	}
	lockouts := make(map[string]time.Time, len(f.Lockouts))
	for k, until := range f.Lockouts {
		lockouts[k] = until
	}
	s.users, s.roles, s.lockouts = users, roles, lockouts
}

// Follow keeps this copy of the model in step with the backend until ctx ends,
// and calls onChange after each change that arrived from elsewhere.
//
// Only changes from elsewhere: a write made here is already in memory, and the
// caller that made it has already done whatever it needed to.
func (s *Store) Follow(ctx context.Context, onChange func()) {
	go s.backend.Watch(ctx, func(data []byte, version string) {
		s.mu.Lock()
		if version == s.version {
			s.mu.Unlock() // our own write, coming back around
			return
		}
		err := s.apply(data, version)
		s.mu.Unlock()

		if err != nil {
			slog.Error("a change to the access model could not be read; this replica is "+
				"still working from the last good copy",
				"object", s.backend.Describe(), "error", err)
			return
		}
		if onChange != nil {
			onChange()
		}
	})
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
	return s.mutate(func() error {
		return s.recordSeen(username, email, provider, groups, initialRoles)
	})
}

// recordSeen is the change itself, run under mutate and possibly more than once.
func (s *Store) recordSeen(username, email, provider string, groups, initialRoles []string) error {
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
		return nil
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
	u.LastSeen = now
	if !changed {
		// Only the timestamp moved, so it waits for the next flush.
		s.seenDirty = true
		return errNoChange
	}
	return nil
}

// FlushSeen writes the last-seen times that have accumulated in memory.
//
// A conflict here loses them: the model is reloaded, and whatever another
// replica wrote wins. That is the right trade — the next request records the
// sighting again, and a timestamp is not worth a fight.
func (s *Store) FlushSeen() error {
	s.mu.RLock()
	dirty := s.seenDirty
	s.mu.RUnlock()
	if !dirty {
		return nil
	}
	if err := s.mutate(func() error { return nil }); err != nil {
		return err
	}
	s.mu.Lock()
	s.seenDirty = false
	s.mu.Unlock()
	return nil
}

// KeepSeen flushes them on a timer until ctx ends, and once more on the way out
// so that a clean shutdown does not throw the last few minutes away.
func (s *Store) KeepSeen(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(seenFlush)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				if err := s.FlushSeen(); err != nil {
					slog.Warn("the last sign-in times could not be written on shutdown", "error", err)
				}
				return
			case <-ticker.C:
				if err := s.FlushSeen(); err != nil {
					slog.Warn("could not write the last sign-in times", "error", err)
				}
			}
		}
	}()
}

// errNoChange ends a mutation that turned out to have nothing to write. Not a
// failure: "this user was already seen a minute ago" is the common case.
var errNoChange = errors.New("nothing changed")

// maxWriteAttempts bounds the retry below. A conflict means another writer got
// there first; a run of them means something is wrong that retrying will not
// fix.
const maxWriteAttempts = 5

// mutate applies a change to the model and writes it back.
//
// The change is passed in rather than made by the caller before calling save,
// because of what happens on a conflict: the model is reloaded and the change
// applied again. Writing back a copy taken before somebody else's edit would
// silently undo it, which is the failure that has no error and no trace.
//
// A change that computes something for its caller must therefore assign rather
// than append: it can run more than once.
func (s *Store) mutate(change func() error) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for attempt := 0; ; attempt++ {
		switch err := change(); {
		case errors.Is(err, errNoChange):
			return nil
		case err != nil:
			return err
		}
		err := s.persist()
		if err == nil {
			return nil
		}
		if !errors.Is(err, ErrConflict) || attempt+1 >= maxWriteAttempts {
			return err
		}
		if err := s.reload(); err != nil {
			return err
		}
	}
}

// persist encodes the model and hands it to the backend. The caller holds the
// lock.
func (s *Store) persist() error {
	data, err := s.encode()
	if err != nil {
		return err
	}
	version, err := s.backend.Save(context.Background(), data, s.version)
	if err != nil {
		return err
	}
	s.version, s.lastWrite = version, nowUTC()
	return nil
}

// encode is the document as it would be written. The caller holds the lock.
func (s *Store) encode() ([]byte, error) {
	data, err := json.MarshalIndent(encodeFile(s.users, s.roles, s.lockouts), "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode the store: %w", err)
	}
	return data, nil
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

// Stat describes the store, for the screen that says where the model lives.
type Stat struct {
	Backend string `json:"backend"`
	// Bytes is what the model occupies where it is kept. Which is what the
	// ceiling applies to, and not the same number as Raw once the backend
	// starts compressing.
	Bytes int `json:"bytes"`
	// Raw is the document before the backend packed it. Equal to Bytes while
	// nothing is being compressed.
	Raw int `json:"raw"`
	// Limit is the backend's ceiling, or zero where it has none worth showing.
	// Shown before it is reached, rather than met as a refused write.
	Limit    int `json:"limit"`
	Users    int `json:"users"`
	WithRole int `json:"withRole"`
	Roles    int `json:"roles"`
	Schema   int `json:"schema"`
	// Current says this replica's copy is the stored one. False means a change
	// landed elsewhere and has not arrived here yet — which is worth seeing,
	// because it is the one thing that can make two replicas disagree.
	Current bool `json:"current"`
	// LastWrite is when this replica last wrote. Zero if it has not since it
	// started, which is normal: most of them only read.
	LastWrite time.Time `json:"lastWrite"`
}

// Limited is a backend with a ceiling worth showing.
type Limited interface{ Limit() int }

// Measured is a backend that knows how much room the model takes where it is
// kept — which is not the size of the document, once it is compressed.
type Measured interface{ StoredBytes() int }

// Stat reads the current state of the store.
func (s *Store) Stat(ctx context.Context) Stat {
	s.mu.RLock()
	data, _ := s.encode()
	st := Stat{
		Backend:   s.backend.Describe(),
		Bytes:     len(data),
		Raw:       len(data),
		Users:     len(s.users),
		Roles:     len(s.roles),
		Schema:    currentVersion,
		LastWrite: s.lastWrite,
	}
	for _, u := range s.users {
		if len(u.Roles) > 0 {
			st.WithRole++
		}
	}
	version := s.version
	s.mu.RUnlock()

	if l, ok := s.backend.(Limited); ok {
		st.Limit = l.Limit()
	}
	// Asked of the backend, and outside the lock: it is a round trip.
	if _, stored, err := s.backend.Load(ctx); err == nil {
		st.Current = stored == version
	}
	// After the read above, so this is what the object holds now rather than
	// what it held when this replica last wrote.
	if m, ok := s.backend.(Measured); ok {
		if n := m.StoredBytes(); n > 0 {
			st.Bytes = n
		}
	}
	return st
}

// Snapshot returns the model exactly as it is stored.
func (s *Store) Snapshot() ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.encode()
}

// Restore replaces the whole model with a snapshot.
//
// Everything: users, roles, and who holds what. The accounts named in
// auth.bootstrapAdmins are not in here and are not touched — which is what
// makes restoring the wrong file recoverable rather than final.
func (s *Store) Restore(data []byte) error {
	// A copy taken out of the cluster may be the compressed form. Accepting it
	// costs one branch and saves somebody working out why the file they just
	// pulled from a Secret is refused.
	if Compressed(data) {
		plain, err := unpack(data)
		if err != nil {
			return err
		}
		data = plain
	}
	var f file
	if err := json.Unmarshal(data, &f); err != nil {
		return fmt.Errorf("this does not look like an access model: %w", err)
	}
	if f.Version > currentVersion {
		return fmt.Errorf("the snapshot is format %d and this build understands %d; "+
			"it was written by a newer version of DevOps Tools", f.Version, currentVersion)
	}
	if f.Users == nil {
		return fmt.Errorf("the snapshot has no users section, so it is not an access model")
	}
	return s.mutate(func() error {
		s.set(f)
		return nil
	})
}
