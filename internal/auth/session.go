package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"log/slog"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"devops-tools/internal/config"
)

// SessionManager issues and verifies the application's own session tokens.
// Used by the local and ldap providers, where no external identity provider
// hands out tokens.
type SessionManager struct {
	key []byte
	ttl time.Duration
}

// sessionClaims is what the session token carries.
//
// The roles in here are not what anything is authorised against: the middleware
// resolves them from the store on every request, so removing a role takes
// effect at once rather than when the session eventually expires. They are
// carried only so a token remains self-describing — useful when reading one by
// hand, and nothing else.
type sessionClaims struct {
	jwt.RegisteredClaims
	Email  string   `json:"email,omitempty"`
	Roles  []string `json:"roles,omitempty"`
	Groups []string `json:"groups,omitempty"`
}

const sessionIssuer = "devops-tools"

// NewSessionManager prepares the signing key. A key supplied through the
// environment is required for multi-replica deployments: without it each
// replica generates its own and sessions break as requests are load balanced.
func NewSessionManager(cfg config.SessionConfig) (*SessionManager, error) {
	key := []byte(cfg.SigningKey)
	if len(key) == 0 {
		buf := make([]byte, 32)
		if _, err := rand.Read(buf); err != nil {
			return nil, fmt.Errorf("generate session key: %w", err)
		}
		key = []byte(base64.StdEncoding.EncodeToString(buf))
		slog.Warn("no session signing key configured, generated a temporary one; " +
			"sessions will not survive a restart and will break with more than one replica " +
			"(set DEVOPS_TOOLS_SESSION_KEY to fix)")
	} else if len(key) < 32 {
		return nil, fmt.Errorf("session signing key is too short: %d bytes, need at least 32", len(key))
	}
	return &SessionManager{key: key, ttl: cfg.TTL}, nil
}

// Issue signs a session token for the user.
func (m *SessionManager) Issue(u *User) (string, time.Time, error) {
	now := time.Now()
	exp := now.Add(m.ttl)
	claims := sessionClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   u.Username,
			Issuer:    sessionIssuer,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(exp),
		},
		Email:  u.Email,
		Roles:  u.Roles,
		Groups: u.Groups,
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(m.key)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("sign session: %w", err)
	}
	return signed, exp, nil
}

// Verify implements tokenVerifier for session tokens.
func (m *SessionManager) Verify(_ context.Context, raw string) (*User, error) {
	var claims sessionClaims
	_, err := jwt.ParseWithClaims(raw, &claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method %v", t.Header["alg"])
		}
		return m.key, nil
	}, jwt.WithIssuer(sessionIssuer), jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}))
	if err != nil {
		return nil, fmt.Errorf("invalid session: %w", err)
	}
	return &User{
		Username: claims.Subject,
		Email:    claims.Email,
		Roles:    claims.Roles,
		Groups:   claims.Groups,
	}, nil
}
