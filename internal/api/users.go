package api

import (
	"net/http"
	"strings"
	"time"
)

// userView is one row of the access overview.
type userView struct {
	Username  string    `json:"username"`
	Email     string    `json:"email,omitempty"`
	Provider  string    `json:"provider"`
	Groups    []string  `json:"groups"`
	Roles     []string  `json:"roles"`
	FirstSeen time.Time `json:"firstSeen"`
	LastSeen  time.Time `json:"lastSeen"`
	// Bootstrap marks a role held through auth.bootstrapAdmins rather than
	// assigned here. Worth showing plainly: it is the one grant the portal
	// cannot revoke, so it should never look like an ordinary assignment.
	Bootstrap bool `json:"bootstrap"`
	// Managed marks an account created here: its password can be changed and it
	// can be deleted. Everything else is a record of someone who signed in.
	Managed bool `json:"managed"`
	// Invited marks roles prepared for a name nobody has signed in under yet.
	// Shown apart from the rest because a wrong name fails silently: it never
	// matches anybody, and the only sign of that is an invitation still sitting
	// here weeks later.
	Invited bool `json:"invited"`
}

// handleUsers lists everyone who has signed in at least once.
//
// This is the whole record: someone appears here the first time they
// authenticate, whether or not they were granted anything, which is what makes
// it possible to grant access to a person rather than to a name typed from
// memory.
func (s *Server) handleUsers(w http.ResponseWriter, r *http.Request) {
	list := s.store.List()
	out := make([]userView, 0, len(list))
	for _, u := range list {
		out = append(out, userView{
			Username:  u.Username,
			Email:     u.Email,
			Provider:  u.Provider,
			Groups:    nonNil(u.Groups),
			Roles:     nonNil(u.Roles),
			FirstSeen: u.FirstSeen,
			LastSeen:  u.LastSeen,
			Bootstrap: s.cfg.Auth.Bootstrap.Grants(u.Username),
			Managed:   u.Managed,
			Invited:   u.Invited(),
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// nonNil keeps empty lists as [] in JSON rather than null, so the UI can map
// over them without a guard at every use.
func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func equalFold(a, b string) bool {
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}
