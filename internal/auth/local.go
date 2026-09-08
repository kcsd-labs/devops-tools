package auth

import (
	"context"
	"fmt"
	"strings"

	"golang.org/x/crypto/bcrypt"

	"devops-tools/internal/config"
)

// localProvider authenticates against accounts held by this deployment rather
// than by a directory: useful for evaluation, small teams, and installations
// with no identity provider at all.
//
// Accounts come from two places. Those in the configuration file belong to the
// deployment and cannot be changed from the UI. Those created in the UI live in
// the store, and are checked first — an account created there is the one an
// administrator can actually manage, so it should win if a name exists in both.
type localProvider struct {
	users map[string]config.LocalUser
	store credentialStore
}

// credentialStore is the portal's own account list. Kept as an interface so the
// provider does not depend on the store package.
type credentialStore interface {
	LocalCredential(username string) (string, bool)
}

// dummyHash is compared against when the username does not exist, so that a
// wrong username costs the same time as a wrong password and cannot be used to
// enumerate accounts. It is the bcrypt hash of a random unused string.
const dummyHash = "$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy"

func newLocalProvider(cfg config.LocalAuthConfig, store credentialStore) (*localProvider, error) {
	users := make(map[string]config.LocalUser, len(cfg.Users))
	for _, u := range cfg.Users {
		key := strings.ToLower(u.Username)
		if _, dup := users[key]; dup {
			return nil, fmt.Errorf("duplicate local user %q", u.Username)
		}
		if _, err := bcrypt.Cost([]byte(u.PasswordHash)); err != nil {
			return nil, fmt.Errorf("user %q: passwordHash is not a valid bcrypt hash: %w", u.Username, err)
		}
		users[key] = u
	}
	return &localProvider{users: users, store: store}, nil
}

var errInvalidCredentials = fmt.Errorf("invalid username or password")

func (p *localProvider) Login(_ context.Context, username, password string) (*User, error) {
	// An account created in the portal takes precedence: it is the one whose
	// password an administrator can change.
	if p.store != nil {
		if hash, ok := p.store.LocalCredential(username); ok {
			if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)); err != nil {
				return nil, errInvalidCredentials
			}
			// Identity only. The roles are the directory's answer, resolved on
			// every request — see Service.rolesFor.
			return &User{Username: username}, nil
		}
	}

	u, ok := p.users[strings.ToLower(username)]
	hash := u.PasswordHash
	if !ok {
		hash = dummyHash
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)); err != nil || !ok {
		return nil, errInvalidCredentials
	}
	return &User{
		Username: u.Username,
		Email:    u.Email,
		Roles:    u.Roles,
	}, nil
}

func (p *localProvider) Close() {}
