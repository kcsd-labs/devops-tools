package api

import (
	"fmt"
	"io"
	"net/http"
	"time"

	"devops-tools/internal/audit"
	"devops-tools/internal/auth"
)

// maxSnapshotBytes bounds what will be read from a restore request. The model
// is measured in tens of kilobytes; anything near this is not one.
const maxSnapshotBytes = 4 << 20

// handleStorageStat says where the access model lives and how much of it there
// is.
//
// Read by whoever may already see the users list: it is the same subject, and
// none of it is the model itself.
func (s *Server) handleStorageStat(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.FromContext(r.Context())
	stat := s.store.Stat(r.Context())

	out := map[string]any{
		"backend":  stat.Backend,
		"bytes":    stat.Bytes,
		"raw":      stat.Raw,
		"limit":    stat.Limit,
		"users":    stat.Users,
		"withRole": stat.WithRole,
		"roles":    stat.Roles,
		"schema":   stat.Schema,
		"current":  stat.Current,
		// Whether this caller may take a snapshot or put one back, so the
		// screen can offer the buttons rather than let them fail.
		"mayRestore": s.authz.AllowedGlobal(user.Roles, OpAccessRestore),
	}
	if !stat.LastWrite.IsZero() {
		out["lastWrite"] = stat.LastWrite.UTC().Format(time.RFC3339)
	}
	// How many replicas are serving. Not the store's business, and only
	// answerable in a cluster — left out where it cannot be found.
	if n, err := s.replicaCount(r.Context()); err == nil {
		out["replicas"] = n
	}
	writeJSON(w, http.StatusOK, out)
}

// handleSnapshot hands back the model as a file.
//
// Behind access-restore rather than user-list: this carries the password hashes
// of accounts created here, which the users screen never shows.
func (s *Server) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.FromContext(r.Context())
	data, err := s.store.Snapshot()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.audit.Log(audit.Entry{
		User: user.Username, Environment: s.cfg.Environment,
		Operation: OpAccessRestore, Target: "snapshot", Allowed: true, Success: true,
	})
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", `attachment; filename="devops-tools-access.json"`)
	_, _ = w.Write(data)
}

// handleRestore replaces the whole model with a snapshot.
func (s *Server) handleRestore(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.FromContext(r.Context())

	data, err := io.ReadAll(io.LimitReader(r.Body, maxSnapshotBytes+1))
	if err != nil {
		http.Error(w, "could not read the snapshot", http.StatusBadRequest)
		return
	}
	if len(data) > maxSnapshotBytes {
		http.Error(w, fmt.Sprintf("that file is larger than %d bytes, which an access model is not",
			maxSnapshotBytes), http.StatusRequestEntityTooLarge)
		return
	}

	before := s.store.Stat(r.Context())
	if err := s.store.Restore(data); err != nil {
		s.audit.Log(audit.Entry{
			User: user.Username, Environment: s.cfg.Environment,
			Operation: OpAccessRestore, Target: "restore", Allowed: true, Success: false,
			Error: err.Error(),
		})
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.authz.Replace(s.store.AccessModel(s.operations))

	after := s.store.Stat(r.Context())
	// Said loudly and with both counts: this is the one action that changes
	// everybody's access at once, and the numbers are what tells somebody they
	// restored the file they meant to.
	s.audit.Log(audit.Entry{
		User: user.Username, Environment: s.cfg.Environment,
		Operation: OpAccessRestore,
		Target:    fmt.Sprintf("restore: %d users and %d roles, was %d and %d", after.Users, after.Roles, before.Users, before.Roles),
		Allowed:   true, Success: true,
	})
	writeJSON(w, http.StatusOK, map[string]any{"users": after.Users, "roles": after.Roles})
}
