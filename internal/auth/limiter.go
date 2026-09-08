package auth

import (
	"sync"
	"time"
)

// Password login is exposed to anyone who can reach the service, so failed
// attempts are throttled per username. This is deliberately simple and
// in-memory: it slows down guessing without adding a datastore dependency.
// Deployments needing cluster-wide limits should put a WAF or ingress rate
// limit in front as well.
const (
	maxFailedAttempts = 5
	lockoutWindow     = 5 * time.Minute
)

type attemptRecord struct {
	failures int
	until    time.Time
}

type loginLimiter struct {
	mu      sync.Mutex
	records map[string]*attemptRecord
}

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{records: make(map[string]*attemptRecord)}
}

// allow reports whether another attempt may be made for this username.
func (l *loginLimiter) allow(username string) bool {
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
	defer l.mu.Unlock()
	r, ok := l.records[username]
	if !ok || time.Now().After(r.until) {
		r = &attemptRecord{}
		l.records[username] = r
	}
	r.failures++
	r.until = time.Now().Add(lockoutWindow)

	// keep the map from growing without bound on username spraying
	if len(l.records) > 10000 {
		now := time.Now()
		for k, v := range l.records {
			if now.After(v.until) {
				delete(l.records, k)
			}
		}
	}
}

// reset clears the counter after a successful login.
func (l *loginLimiter) reset(username string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.records, username)
}
