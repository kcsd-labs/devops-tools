package auth

import (
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Tickets: facts the server established, handed to the browser and taken back.
//
// Some work spans several requests — start a build, then poll it, then start
// the deploy it is waiting on. The later requests need to know which project
// and pipeline, and the browser is the obvious place to keep that. It is also
// the wrong place to trust it from: a project name sent by a client is a
// project the client chose, and acting on it means acting anywhere the token
// can reach, however narrow the caller's actual access is.
//
// So the server signs what it worked out, and will only act on what comes back
// bearing its own signature.

const ticketIssuer = "devops-tools/ticket"

// IssueTicket and VerifyTicket on the service, so callers need not reach past
// it for the signing key. Under a token-based provider there is still a session
// manager — the portal signs its own tickets whoever authenticates people.

func (s *Service) IssueTicket(t Ticket, ttl time.Duration) (string, error) {
	if s.sessions == nil {
		return "", fmt.Errorf("no signing key available for tickets")
	}
	return s.sessions.IssueTicket(t, ttl)
}

func (s *Service) VerifyTicket(raw, purpose string) (Ticket, error) {
	if s.sessions == nil {
		return Ticket{}, fmt.Errorf("no signing key available for tickets")
	}
	return s.sessions.VerifyTicket(raw, purpose)
}

// Ticket is one such established fact.
type Ticket struct {
	// Purpose keeps a ticket for one job from being presented for another.
	Purpose  string `json:"pur"`
	Project  string `json:"prj"`
	Pipeline int    `json:"pl,omitempty"`
	Job      int    `json:"job,omitempty"`
	// Environment decides which jobs may be released, so it belongs here rather
	// than in a parameter: sent by the caller, it would select another
	// environment's job names and release one of those instead.
	Environment string `json:"env,omitempty"`
	// Namespace and Pod record where this came from, for the audit trail: the
	// pod may be gone by the time the build finishes.
	Namespace string `json:"ns,omitempty"`
	Pod       string `json:"pod,omitempty"`
}

type ticketClaims struct {
	jwt.RegisteredClaims
	Ticket
}

// IssueTicket signs a ticket that expires after ttl.
//
// Short-lived on purpose: it authorises acting on a project, and the work it
// covers takes minutes. A ticket that outlived the build would be a standing
// permission nobody remembers granting.
func (m *SessionManager) IssueTicket(t Ticket, ttl time.Duration) (string, error) {
	now := time.Now()
	claims := ticketClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    ticketIssuer,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
		},
		Ticket: t,
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(m.key)
	if err != nil {
		return "", fmt.Errorf("sign ticket: %w", err)
	}
	return signed, nil
}

// VerifyTicket returns the ticket if it was signed here, has not expired, and
// was issued for this purpose.
func (m *SessionManager) VerifyTicket(raw, purpose string) (Ticket, error) {
	var claims ticketClaims
	_, err := jwt.ParseWithClaims(raw, &claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method %v", t.Header["alg"])
		}
		return m.key, nil
	}, jwt.WithIssuer(ticketIssuer), jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}))
	if err != nil {
		return Ticket{}, fmt.Errorf("invalid ticket: %w", err)
	}
	if claims.Purpose != purpose {
		// A ticket to watch a build is not a ticket to start a deploy. Without
		// this the narrower one could be presented for the wider.
		return Ticket{}, fmt.Errorf("this ticket is for %q, not %q", claims.Purpose, purpose)
	}
	return claims.Ticket, nil
}
