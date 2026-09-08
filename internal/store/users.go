package store

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

var (
	// ErrNotFound is returned for a user or role that is not on record.
	ErrNotFound = errors.New("not found")
	// ErrExists is returned when creating something that is already there.
	ErrExists = errors.New("already exists")
	// ErrNotManaged is returned for an account this portal does not own: one
	// defined in the configuration file, or one that belongs to a directory.
	// Its password lives elsewhere and cannot be changed from here.
	ErrNotManaged = errors.New("this account is not managed by the portal")
)

// usernamePattern is deliberately narrow. The name ends up in audit records, in
// URLs and in comparisons against a directory, so anything that would need
// escaping somewhere is refused at the point it is typed.
var usernamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{1,63}$`)

// ValidateUsername reports whether a name may be created.
func ValidateUsername(name string) error {
	if !usernamePattern.MatchString(name) {
		return fmt.Errorf("a username must be 2 to 64 characters of letters, digits, dot, dash " +
			"or underscore, and must start with a letter or digit")
	}
	return nil
}

// CreateLocalUser adds an account that signs in with a password held here.
//
// The account exists before anyone signs in, which is the difference from every
// other user in this store: with no directory to invite people from, someone
// has to be able to hand out credentials.
func (s *Store) CreateLocalUser(username, email, passwordHash string, roles []string) error {
	if err := ValidateUsername(username); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.users[key(username)]; ok {
		return fmt.Errorf("user %q: %w", username, ErrExists)
	}
	now := nowUTC()
	s.users[key(username)] = &User{
		Username:     username,
		Email:        email,
		Provider:     "local",
		Roles:        roles,
		PasswordHash: passwordHash,
		Managed:      true,
		FirstSeen:    now,
		// Not LastSeen: they have not signed in yet, and claiming otherwise
		// would misreport exactly what this column is for.
	}
	return s.save()
}

// SetPassword replaces the password of an account created here.
// Invite grants roles to somebody who has not signed in yet.
//
// Under a directory provider people appear here only after authenticating once,
// so until then there is nobody to grant anything to — which makes preparing
// access for a new colleague impossible without this. The record carries roles
// and nothing else: no password, so it cannot be signed in to, and no provider,
// because which one they will use is not known until they arrive.
//
// It becomes an ordinary account the first time the name matches somebody
// authenticating. Until then it is a standing grant for a name, which is why
// an unclaimed one is worth showing plainly and worth being able to delete.
func (s *Store) Invite(username, email string, roles []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.users[key(username)]; exists {
		return fmt.Errorf("user %q: %w", username, ErrExists)
	}
	s.users[key(username)] = &User{
		Username:  username,
		Email:     email,
		Roles:     roles,
		FirstSeen: nowUTC(),
		// Zero: nobody has signed in as this name. That is what separates an
		// invitation from a record of a person, everywhere it matters.
		LastSeen: time.Time{},
	}
	return s.save()
}

// Invited reports whether a record is an invitation nobody has claimed.
func (u *User) Invited() bool { return u.LastSeen.IsZero() }

func (s *Store) SetPassword(username, passwordHash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	u, ok := s.users[key(username)]
	if !ok {
		return fmt.Errorf("user %q: %w", username, ErrNotFound)
	}
	if !u.Managed {
		return fmt.Errorf("user %q: %w", username, ErrNotManaged)
	}
	u.PasswordHash = passwordHash
	return s.save()
}

// SetRoles replaces a user's roles. An empty list revokes every one of them,
// which takes effect on the caller's next request — roles are not carried in
// the session.
func (s *Store) SetRoles(username string, roles []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	u, ok := s.users[key(username)]
	if !ok {
		return fmt.Errorf("user %q: %w", username, ErrNotFound)
	}
	u.Roles = roles
	return s.save()
}

// DeleteUser removes a record entirely: the account, or the history of somebody
// who signed in, along with whatever roles it held.
//
// Any record, not only accounts created here. Refusing the rest was too clever:
// somebody who has left cannot sign in, so their row would sit in the list for
// ever with no way to remove it. Somebody who has not left comes back on their
// next sign-in with no roles — which is a revocation, plainly, and the
// interface says so before doing it.
func (s *Store) DeleteUser(username string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.users[key(username)]; !ok {
		return fmt.Errorf("user %q: %w", username, ErrNotFound)
	}
	delete(s.users, key(username))
	return s.save()
}

// LocalCredential returns the stored password hash for an account created here.
// Used by the local provider, which checks this before the configuration file.
func (s *Store) LocalCredential(username string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	u, ok := s.users[key(username)]
	if !ok || !u.Managed || u.PasswordHash == "" {
		return "", false
	}
	return u.PasswordHash, true
}

// Get returns one user.
func (s *Store) Get(username string) (User, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	u, ok := s.users[key(username)]
	if !ok {
		return User{}, false
	}
	return *u, true
}

// NormaliseRoles trims, drops blanks and removes duplicates, so that what is
// stored is what the access model will actually match against.
func NormaliseRoles(roles []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, r := range roles {
		r = strings.TrimSpace(r)
		if r == "" || seen[r] {
			continue
		}
		seen[r] = true
		out = append(out, r)
	}
	return out
}
