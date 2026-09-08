package auth

import (
	"context"
	"testing"
	"time"

	"devops-tools/internal/config"
)

func testManager(t *testing.T) *SessionManager {
	t.Helper()
	m, err := NewSessionManager(config.SessionConfig{
		SigningKey: "0123456789abcdef0123456789abcdef",
		TTL:        time.Hour,
	})
	if err != nil {
		t.Fatalf("NewSessionManager: %v", err)
	}
	return m
}

// The whole point: a project the client made up is refused. Accepting one would
// mean acting anywhere the GitLab token reaches, whatever the caller's own
// access actually is.
func TestAnUnsignedTicketIsRefused(t *testing.T) {
	m := testManager(t)
	for _, raw := range []string{
		"",
		"not-a-token",
		// A well-formed token signed with a different key.
		"eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJwdXIiOiJyZWJ1aWxkIiwicHJqIjoic29tZW9uZS9lbHNlIn0." +
			"3wLj0y0Xl0z0zvJqQ0iVQ8kQd0mMv3rN6QY0F0mQ0aE",
	} {
		if _, err := m.VerifyTicket(raw, "rebuild"); err == nil {
			t.Errorf("VerifyTicket(%q) accepted something it did not sign", raw)
		}
	}
}

func TestASignedTicketComesBackIntact(t *testing.T) {
	m := testManager(t)
	want := Ticket{
		Purpose: "rebuild", Project: "g/p/svc", Pipeline: 4321, Job: 99,
		Namespace: "payments-dev", Pod: "svc-abc",
	}
	raw, err := m.IssueTicket(want, time.Hour)
	if err != nil {
		t.Fatalf("IssueTicket: %v", err)
	}
	got, err := m.VerifyTicket(raw, "rebuild")
	if err != nil {
		t.Fatalf("VerifyTicket: %v", err)
	}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

// A ticket for watching a build is not a ticket for starting a deploy. Without
// the check the narrower one could be presented for the wider.
func TestATicketIsOnlyGoodForItsPurpose(t *testing.T) {
	m := testManager(t)
	raw, err := m.IssueTicket(Ticket{Purpose: "watch", Project: "g/p"}, time.Hour)
	if err != nil {
		t.Fatalf("IssueTicket: %v", err)
	}
	if _, err := m.VerifyTicket(raw, "play-deploy"); err == nil {
		t.Error("a watch ticket was accepted for starting a deploy")
	}
}

// It authorises acting on a project, and the work it covers takes minutes. A
// ticket that outlived the build would be a standing permission nobody
// remembers granting.
func TestAnExpiredTicketIsRefused(t *testing.T) {
	m := testManager(t)
	raw, err := m.IssueTicket(Ticket{Purpose: "rebuild", Project: "g/p"}, -time.Second)
	if err != nil {
		t.Fatalf("IssueTicket: %v", err)
	}
	if _, err := m.VerifyTicket(raw, "rebuild"); err == nil {
		t.Error("an expired ticket was accepted")
	}
}

// Session tokens and tickets are signed with the same key, so they must not be
// interchangeable: a session is not permission to act on a project, and a
// ticket must never authenticate anybody.
func TestSessionsAndTicketsAreNotInterchangeable(t *testing.T) {
	m := testManager(t)

	session, _, err := m.Issue(&User{Username: "ada", Roles: []string{"platform-admin"}})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if _, err := m.VerifyTicket(session, "rebuild"); err == nil {
		t.Error("a session token was accepted as a ticket")
	}

	ticket, err := m.IssueTicket(Ticket{Purpose: "rebuild", Project: "g/p"}, time.Hour)
	if err != nil {
		t.Fatalf("IssueTicket: %v", err)
	}
	if u, err := m.Verify(context.Background(), ticket); err == nil {
		t.Errorf("a ticket authenticated as %+v", u)
	}
}
