package auth

import (
	"testing"
	"time"
)

// sharedLockouts stands in for the directory, and counts the writes: sharing
// every attempt rather than every lockout is the mistake worth guarding.
type sharedLockouts struct {
	until  map[string]time.Time
	known  map[string]bool
	writes int
}

func newSharedLockouts(known ...string) *sharedLockouts {
	s := &sharedLockouts{until: map[string]time.Time{}, known: map[string]bool{}}
	for _, k := range known {
		s.known[k] = true
	}
	return s
}

func (s *sharedLockouts) LockedUntil(username string) time.Time {
	until := s.until[username]
	if until.Before(time.Now()) {
		return time.Time{}
	}
	return until
}

func (s *sharedLockouts) LockUntil(username string, until time.Time) error {
	s.writes++
	s.until[username] = until
	return nil
}

func (s *sharedLockouts) Unlock(username string) error {
	s.writes++
	delete(s.until, username)
	return nil
}

func (s *sharedLockouts) Known(username string) bool { return s.known[username] }

func TestALockoutIsSharedOnceTheAllowanceIsGone(t *testing.T) {
	shared := newSharedLockouts("anna")
	l := newLoginLimiter(shared)

	for i := 0; i < maxFailedAttempts-1; i++ {
		l.fail("anna")
		if shared.writes != 0 {
			t.Fatalf("shared after %d attempts; only the lockout is worth a write", i+1)
		}
	}
	l.fail("anna")
	if shared.writes != 1 {
		t.Fatalf("writes=%d, want one when the allowance runs out", shared.writes)
	}

	// Further attempts extend nothing: another write per attempt is what an
	// attacker would be aiming for.
	l.fail("anna")
	l.fail("anna")
	if shared.writes != 1 {
		t.Fatalf("writes=%d after more attempts, want one", shared.writes)
	}
}

func TestANameWithNoAccountIsNotWrittenToTheCluster(t *testing.T) {
	shared := newSharedLockouts() // nobody is known
	l := newLoginLimiter(shared)

	for i := 0; i < maxFailedAttempts*3; i++ {
		l.fail("no-such-person")
	}
	if shared.writes != 0 {
		t.Fatalf("writes=%d; spraying names that do not exist must not reach the cluster", shared.writes)
	}
	// It is still shut out here, on this replica.
	if l.allow("no-such-person") {
		t.Fatal("the local counter did not shut it out")
	}
}

func TestAnotherReplicasLockoutIsHonoured(t *testing.T) {
	shared := newSharedLockouts("anna")
	// Decided elsewhere: this replica has seen nothing.
	if err := shared.LockUntil("anna", time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	l := newLoginLimiter(shared)
	if l.allow("anna") {
		t.Fatal("an attempt was allowed although another replica had shut the account out")
	}
}

func TestAnElapsedLockoutStopsHolding(t *testing.T) {
	shared := newSharedLockouts("anna")
	if err := shared.LockUntil("anna", time.Now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	if !newLoginLimiter(shared).allow("anna") {
		t.Fatal("a lockout that has run out is still holding")
	}
}

func TestASuccessfulSignInClearsTheSharedLockout(t *testing.T) {
	shared := newSharedLockouts("anna")
	l := newLoginLimiter(shared)
	for i := 0; i < maxFailedAttempts; i++ {
		l.fail("anna")
	}
	before := shared.writes

	l.reset("anna")
	if shared.writes != before+1 {
		t.Fatalf("writes=%d, want one more: the lockout should be cleared", shared.writes)
	}
	if !l.allow("anna") {
		t.Fatal("still shut out after signing in")
	}

	// And a sign-in with nothing to clear writes nothing.
	l.reset("anna")
	if shared.writes != before+1 {
		t.Fatalf("clearing an absent lockout wrote anyway")
	}
}

func TestWithoutASharedDirectoryTheLimitIsStillLocal(t *testing.T) {
	// A single replica, or a directory that cannot hold lockouts: behaviour is
	// what it always was.
	l := newLoginLimiter(nil)
	for i := 0; i < maxFailedAttempts; i++ {
		l.fail("anna")
	}
	if l.allow("anna") {
		t.Fatal("the local counter did not shut the account out")
	}
}
