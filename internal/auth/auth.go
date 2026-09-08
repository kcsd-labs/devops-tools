// Package auth authenticates users through one of the supported providers
// (local, ldap, oidc) and exposes the result to the HTTP layer.
//
// Two authentication styles are supported:
//
//   - Token verification (oidc): an external identity provider issues the token,
//     the application only verifies it on every request.
//   - Credential login (local, ldap): the application verifies the credentials
//     itself and issues its own signed session token.
//
// Both end up in the same place: a *User in the request context.
package auth

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"devops-tools/internal/config"
)

// User is an authenticated caller.
type User struct {
	Username string   `json:"username"`
	Email    string   `json:"email"`
	Roles    []string `json:"roles"`
	Groups   []string `json:"groups"`
}

type ctxKey int

const userCtxKey ctxKey = 0

// FromContext returns the user placed in the context by Middleware.
func FromContext(ctx context.Context) (*User, bool) {
	u, ok := ctx.Value(userCtxKey).(*User)
	return u, ok
}

// tokenVerifier validates a bearer token presented on a request.
type tokenVerifier interface {
	Verify(ctx context.Context, rawToken string) (*User, error)
}

// loginProvider validates a username and password.
type loginProvider interface {
	Login(ctx context.Context, username, password string) (*User, error)
	Close()
}

// Directory is where roles come from, and where signing in is recorded.
//
// Roles are resolved on every request rather than carried in the session. That
// is what makes revoking access take effect at once: a role embedded in a token
// would outlive its removal by however long the session had left to run, which
// for an eight-hour session is most of a working day.
type Directory interface {
	// RecordSeen registers a successful authentication. initialRoles apply
	// only to a user met for the first time.
	RecordSeen(username, email, provider string, groups, initialRoles []string) error
	RolesFor(username string) []string
	GroupsFor(username string) []string
}

// Service is the façade used by the API layer.
type Service struct {
	kind     string
	verifier tokenVerifier
	login    loginProvider // nil when the provider is token-based
	sessions *SessionManager
	limiter  *loginLimiter
	oidcInfo *OIDCPublicInfo

	dir       Directory
	bootstrap config.BootstrapConfig
}

// PublicInfo is returned by the unauthenticated /api/auth/info endpoint so the
// frontend knows whether to render a login form or redirect to an IdP.
type PublicInfo struct {
	Provider string          `json:"provider"`
	OIDC     *OIDCPublicInfo `json:"oidc,omitempty"`
	// PasswordLogin reports that a password form exists. Under oidc that means
	// the break-glass account, which the sign-in screen keeps off the main path
	// but has to be able to reach. Only the fact is exposed, never the name.
	PasswordLogin bool `json:"passwordLogin"`
}

// OIDCPublicInfo is what the browser needs to start an OIDC flow.
type OIDCPublicInfo struct {
	IssuerURL string `json:"issuerUrl"`
	ClientID  string `json:"clientId"`
	// DisplayName goes on the sign-in button.
	DisplayName string `json:"displayName"`
}

// New builds the service for the configured provider. dir supplies roles and
// records sign-ins.
func New(ctx context.Context, cfg config.AuthConfig, dir Directory) (*Service, error) {
	s := &Service{
		kind:      cfg.Provider,
		limiter:   newLoginLimiter(),
		dir:       dir,
		bootstrap: cfg.Bootstrap,
	}

	switch cfg.Provider {
	case config.ProviderOIDC:
		v, err := newOIDCVerifier(ctx, cfg.OIDC)
		if err != nil {
			return nil, err
		}
		s.verifier = v
		s.oidcInfo = &OIDCPublicInfo{
			IssuerURL:   cfg.OIDC.IssuerURL,
			ClientID:    cfg.OIDC.ClientID,
			DisplayName: cfg.OIDC.DisplayName,
		}

		// The break-glass account signs in with a password, so this deployment
		// issues its own tokens as well as verifying the provider's. Requests
		// then arrive carrying either, and both are verified in full — see
		// Middleware.
		if cfg.Bootstrap.PasswordLogin.Enabled() {
			sessions, err := NewSessionManager(cfg.Session)
			if err != nil {
				return nil, err
			}
			s.sessions = sessions
		}

	case config.ProviderLocal, config.ProviderLDAP:
		sessions, err := NewSessionManager(cfg.Session)
		if err != nil {
			return nil, err
		}
		s.sessions = sessions
		s.verifier = sessions

		if cfg.Provider == config.ProviderLocal {
			creds, _ := dir.(credentialStore)
			p, err := newLocalProvider(cfg.Local, creds)
			if err != nil {
				return nil, err
			}
			s.login = p
		} else {
			p, err := newLDAPProvider(cfg.LDAP)
			if err != nil {
				return nil, err
			}
			s.login = p
		}

	default:
		return nil, fmt.Errorf("unknown auth provider %q", cfg.Provider)
	}
	return s, nil
}

// Kind reports the configured provider.
func (s *Service) Kind() string { return s.kind }

// SupportsLogin reports whether username/password login applies at all.
func (s *Service) SupportsLogin() bool { return s.login != nil || s.bootstrapLoginEnabled() }

// PasswordLoginEnabled reports whether the break-glass account is configured.
// The sign-in screen uses it to offer that route under a provider which would
// otherwise have no password form at all.
func (s *Service) PasswordLoginEnabled() bool { return s.bootstrapLoginEnabled() }

func (s *Service) bootstrapLoginEnabled() bool {
	return s.bootstrap.PasswordLogin.Enabled() && s.sessions != nil
}

// PublicInfo describes the provider to unauthenticated clients.
func (s *Service) PublicInfo() PublicInfo {
	return PublicInfo{Provider: s.kind, OIDC: s.oidcInfo, PasswordLogin: s.SupportsLogin()}
}

// Close releases provider resources (LDAP connection pools).
func (s *Service) Close() {
	if s.login != nil {
		s.login.Close()
	}
}

// ErrLoginNotSupported is returned when /api/login is called on an OIDC setup.
var ErrLoginNotSupported = fmt.Errorf("password login is not enabled for this provider")

// ErrTooManyAttempts is returned once the per-user rate limit kicks in.
var ErrTooManyAttempts = fmt.Errorf("too many failed attempts, try again later")

// Login verifies credentials and issues a session token.
func (s *Service) Login(ctx context.Context, username, password string) (token string, expiresAt time.Time, user *User, err error) {
	if !s.SupportsLogin() {
		return "", time.Time{}, nil, ErrLoginNotSupported
	}
	if !s.limiter.allow(username) {
		return "", time.Time{}, nil, ErrTooManyAttempts
	}
	// The break-glass account first, and without consulting the provider: the
	// case it exists for is the provider being unreachable.
	if u, ok := s.bootstrapLogin(username, password); ok {
		s.limiter.reset(username)
		s.register(u)
		u.Roles = s.rolesFor(u.Username)
		token, expiresAt, err = s.sessions.Issue(u)
		if err != nil {
			return "", time.Time{}, nil, err
		}
		slog.Warn("break-glass sign-in used", "user", u.Username)
		return token, expiresAt, u, nil
	}

	if s.login == nil {
		s.limiter.fail(username)
		return "", time.Time{}, nil, errBadCredentials
	}
	u, err := s.login.Login(ctx, username, password)
	if err != nil {
		s.limiter.fail(username)
		return "", time.Time{}, nil, err
	}
	s.limiter.reset(username)

	// The provider has established who this is; what they may do is the
	// directory's answer, so resolve it here too rather than trusting whatever
	// roles the provider happened to carry.
	s.register(u)
	u.Roles = s.rolesFor(u.Username)

	token, expiresAt, err = s.sessions.Issue(u)
	if err != nil {
		return "", time.Time{}, nil, err
	}
	return token, expiresAt, u, nil
}

// register records the sign-in. The roles the provider supplied become the
// user's starting point, but only the first time they are seen — after that
// the portal's assignment is authoritative and must not be overwritten.
func (s *Service) register(u *User) {
	if s.dir == nil {
		return
	}
	if err := s.dir.RecordSeen(u.Username, u.Email, s.kind, u.Groups, u.Roles); err != nil {
		// Not fatal: being unable to record the sighting must not stop someone
		// signing in. It does mean the volume is unwritable, so say so loudly.
		slog.Error("record sign-in", "user", u.Username, "err", err)
	}
}

// rolesFor resolves a user's effective roles: what the directory holds, plus
// the bootstrap grant. The bootstrap grant is deliberately not stored — it is
// the way back in when the stored model locks everyone out, so it has to be
// something only a change to the deployment can alter.
func (s *Service) rolesFor(username string) []string {
	var roles []string
	if s.dir != nil {
		roles = s.dir.RolesFor(username)
	}
	if s.bootstrap.Grants(username) {
		seen := map[string]bool{}
		for _, r := range roles {
			seen[r] = true
		}
		for _, r := range s.bootstrap.Roles {
			if !seen[r] {
				roles = append(roles, r)
			}
		}
	}
	return roles
}

// Middleware extracts and verifies the bearer token, then stores the user in
// the request context.
func (s *Service) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := bearerToken(r)
		if raw == "" {
			http.Error(w, "missing bearer token", http.StatusUnauthorized)
			return
		}
		user, err := s.verifier.Verify(r.Context(), raw)
		if err != nil && s.kind == config.ProviderOIDC && s.sessions != nil {
			// Might be one this deployment issued to the break-glass account.
			// Both paths verify the signature in full; neither accepts a token
			// the other would have rejected.
			//
			// Only a success is taken from the second attempt. Its failure is
			// always the same complaint about the signing algorithm — the two
			// kinds of token are signed differently — and reporting that in
			// place of what the provider actually said would describe an
			// expired token, or one belonging to another client, as a broken
			// signature.
			if u, sessErr := s.sessions.Verify(r.Context(), raw); sessErr == nil {
				user, err = u, nil
			}
		}
		if err != nil {
			// The reason never reaches the caller — deliberately — so it has to
			// reach the log, or a rejection every token trips over looks the
			// same as an ordinary expiry.
			slog.Warn("rejected a bearer token", "provider", s.kind, "err", err)
			http.Error(w, "invalid token", http.StatusUnauthorized)
			return
		}

		// The token says who; the directory says what they may do, as of now.
		if s.kind == config.ProviderOIDC {
			// A token-based provider has no sign-in event to hook, so this is
			// the only place the user can be registered at all. RecordSeen
			// writes rarely enough for that to be cheap — see seenResolution.
			s.register(user)
		}
		user.Roles = s.rolesFor(user.Username)
		if len(user.Groups) == 0 && s.dir != nil {
			// A session token carries no groups; show what was last recorded.
			user.Groups = s.dir.GroupsFor(user.Username)
		}

		ctx := context.WithValue(r.Context(), userCtxKey, user)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if len(h) > 7 && strings.EqualFold(h[:7], "bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return ""
}

// errBadCredentials is deliberately the same message the providers use: which
// account exists, and by which route, is not something a sign-in form should
// reveal.
var errBadCredentials = fmt.Errorf("invalid username or password")

// bootstrapLogin checks the break-glass credentials.
//
// The comparison runs even when the username does not match, against a hash
// that nothing can satisfy, so that a wrong name costs the same as a wrong
// password and cannot be used to find out which name is the right one.
func (s *Service) bootstrapLogin(username, password string) (*User, bool) {
	if !s.bootstrapLoginEnabled() {
		return nil, false
	}
	l := s.bootstrap.PasswordLogin
	hash := l.PasswordHash
	match := strings.EqualFold(strings.TrimSpace(username), strings.TrimSpace(l.Username))
	if !match {
		hash = dummyHash
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)); err != nil || !match {
		return nil, false
	}
	return &User{Username: l.Username}, true
}
