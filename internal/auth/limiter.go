package auth

import (
	"log/slog"
	"sync"
	"time"
)

// Password login is exposed to anyone who can reach the service, so failed
// attempts are throttled per username.
//
// Counting is per replica and in memory. The lockout itself is shared, where
// the directory can hold one: otherwise three replicas would mean three times
// the attempts before anybody is shut out. Sharing every failed attempt instead
// would turn each one into a write to the cluster, which is what an attacker
// would choose. A deployment wanting a cluster-wide rate limit should still put
// one in front, at the ingress.
const (
	maxFailedAttempts = 5
	lockoutWindow     = 5 * time.Minute
)

type attemptRecord struct {
	failures int
	until    time.Time
}

// Lockouts is the part of a directory that can hold a lockout for every replica
// to see. Optional: without it the limit is per replica, which is what a single
// one has always had.
type Lockouts interface {
	// LockedUntil reports when an account stops being locked out, or zero.
	LockedUntil(username string) time.Time
	// LockUntil records a lockout.
	LockUntil(username string, until time.Time) error
	// Unlock clears one after a successful sign-in.
	Unlock(username string) error
	// Known reports whether the account exists.
	Known(username string) bool
}

type loginLimiter struct {
	mu      sync.Mutex
	records map[string]*attemptRecord
	shared  Lockouts // nil when the directory cannot hold them
}

func newLoginLimiter(shared Lockouts) *loginLimiter {
	return &loginLimiter{records: make(map[string]*attemptRecord), shared: shared}
}

// allow reports whether another attempt may be made for this username.
func (l *loginLimiter) allow(username string) bool {
	// A lockout decided by another replica counts here too. Read from memory,
	// so this costs nothing on the request path.
	if l.shared != nil && !l.shared.LockedUntil(username).IsZero() {
		return false
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	r, ok := l.records[username]
	if !ok {
		return true
	}
	if time.Now().After(r.until) {
		delete(l.records, username) // window elapsed, start clean
		return true
	}
	return r.failures < maxFailedAttempts
}

// fail records an unsuccessful attempt and extends the window.
func (l *loginLimiter) fail(username string) {
	l.mu.Lock()
	r, ok := l.records[username]
	if !ok || time.Now().After(r.until) {
		r = &attemptRecord{}
		l.records[username] = r
	}
	r.failures++
	r.until = time.Now().Add(lockoutWindow)
	reached := r.failures >= maxFailedAttempts

	// keep the map from growing without bound on username spraying
	if len(l.records) > 10000 {
		now := time.Now()
		for k, v := range l.records {
			if now.After(v.until) {
				delete(l.records, k)
			}
		}
	}
	l.mu.Unlock()

	// Outside the lock: this one writes, and holding the lock across it would
	// stall every other sign-in for the length of a round trip.
	if reached {
		l.share(username)
	}
}

// share records the lockout where the other replicas can see it.
//
// Only for accounts that exist. Somebody spraying names that do not would
// otherwise turn every fifth request into a write to the cluster, and shutting
// out a name nobody can sign in as protects nothing — the local counter is
// already slowing them down on every replica they reach.
func (l *loginLimiter) share(username string) {
	if l.shared == nil || !l.shared.Known(username) {
		return
	}
	if !l.shared.LockedUntil(username).IsZero() {
		return // already shut out; extending it is not worth a write
	}
	if err := l.shared.LockUntil(username, time.Now().Add(lockoutWindow)); err != nil {
		slog.Warn("a lockout could not be shared with the other replicas; it still holds here",
			"user", username, "error", err)
	}
}

// reset clears the counter after a successful login.
func (l *loginLimiter) reset(username string) {
	l.mu.Lock()
	delete(l.records, username)
	l.mu.Unlock()

	if l.shared != nil && !l.shared.LockedUntil(username).IsZero() {
		if err := l.shared.Unlock(username); err != nil {
			slog.Warn("a lockout could not be cleared", "user", username, "error", err)
		}
	}
}
