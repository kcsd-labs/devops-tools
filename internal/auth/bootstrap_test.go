package auth

import (
	"context"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"devops-tools/internal/config"
)

func hashOf(t *testing.T, pw string) string {
	t.Helper()
	h, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	return string(h)
}

func bootstrapService(t *testing.T, username, password string) *Service {
	t.Helper()
	sessions, err := NewSessionManager(config.SessionConfig{
		TTL:        time.Hour,
		SigningKey: "0123456789abcdef0123456789abcdef",
	})
	if err != nil {
		t.Fatal(err)
	}
	return &Service{
		kind:     config.ProviderOIDC,
		limiter:  newLoginLimiter(),
		sessions: sessions,
		dir:      newDir(map[string][]string{}),
		bootstrap: config.BootstrapConfig{
			Users: []string{username},
			Roles: []string{"platform-admin"},
			PasswordLogin: config.BootstrapLogin{
				Username:     username,
				PasswordHash: hashOf(t, password),
			},
		},
	}
}

// The whole point: a password sign-in that works when the provider cannot be
// used at all. Under oidc there is no login provider, so this is the only path.
func TestBootstrapSignsInWithoutAProvider(t *testing.T) {
	s := bootstrapService(t, "admin", "correct horse")

	if !s.SupportsLogin() {
		t.Fatal("a provider with no password login should still offer the break-glass one")
	}
	token, _, user, err := s.Login(context.Background(), "admin", "correct horse")
	if err != nil {
		t.Fatalf("sign-in failed: %v", err)
	}
	if token == "" {
		t.Fatal("no token issued")
	}
	// And it carries the roles the bootstrap grant gives, or signing in would
	// land on an empty portal.
	if len(user.Roles) != 1 || user.Roles[0] != "platform-admin" {
		t.Fatalf("roles = %v", user.Roles)
	}
}

func TestBootstrapRejectsWrongPassword(t *testing.T) {
	s := bootstrapService(t, "admin", "correct horse")
	if _, _, _, err := s.Login(context.Background(), "admin", "wrong"); err == nil {
		t.Fatal("a wrong password was accepted")
	}
}

func TestBootstrapRejectsOtherUsernames(t *testing.T) {
	s := bootstrapService(t, "admin", "correct horse")
	// Even with the right password: the credential belongs to one name.
	if _, _, _, err := s.Login(context.Background(), "ivan", "correct horse"); err == nil {
		t.Fatal("another username was accepted with the break-glass password")
	}
}

func TestBootstrapUsernameIsCaseInsensitive(t *testing.T) {
	s := bootstrapService(t, "admin", "correct horse")
	if _, _, _, err := s.Login(context.Background(), "ADMIN", "correct horse"); err != nil {
		t.Fatalf("case-sensitive username comparison: %v", err)
	}
}

// Not configured means not available — this must never be a way in that
// appears by default.
func TestBootstrapOffByDefault(t *testing.T) {
	s := &Service{kind: config.ProviderOIDC, limiter: newLoginLimiter()}
	if s.SupportsLogin() {
		t.Fatal("password login is offered without being configured")
	}
	if _, _, _, err := s.Login(context.Background(), "admin", "anything"); err == nil {
		t.Fatal("sign-in succeeded with no credentials configured")
	}
}

// A hash without a username, or the reverse, must not half-enable it.
func TestBootstrapNeedsBothHalves(t *testing.T) {
	for _, l := range []config.BootstrapLogin{
		{Username: "admin"},
		{PasswordHash: hashOf(t, "x")},
	} {
		if l.Enabled() {
			t.Fatalf("%+v reported itself enabled", l)
		}
	}
}

// The rate limiter has to cover this path too: it is a password form reachable
// without the provider, which is exactly what gets guessed at.
func TestBootstrapIsRateLimited(t *testing.T) {
	s := bootstrapService(t, "admin", "correct horse")
	for i := 0; i < 5; i++ {
		if _, _, _, err := s.Login(context.Background(), "admin", "wrong"); err == nil {
			t.Fatal("a wrong password was accepted")
		}
	}
	_, _, _, err := s.Login(context.Background(), "admin", "correct horse")
	if err != ErrTooManyAttempts {
		t.Fatalf("expected the limiter to bite, got %v", err)
	}
}
