package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"devops-tools/internal/audit"
	"devops-tools/internal/auth"
	"devops-tools/internal/configsource"
)

// The Configurations page: the service configuration, from Consul or from the
// repository a synchroniser copies into it.
//
// Access is granted by path rather than by namespace, because the two do not
// line up — a team's namespaces and the folders its configuration lives in are
// named independently. See rbac.AllowedConfig.

// configEntry is one entry as the browser sees it.
type configEntry struct {
	Path string `json:"path"`
	// Writable saves the browser from offering an editor that would be refused
	// on save. Read and write are separate grants.
	Writable bool `json:"writable"`
}

// handleListConfigs lists the entries the caller may see, and nothing else.
//
// The filtering happens here rather than in the browser: a path that is not
// granted should not reach the client at all, since the names alone say what
// systems exist.
func (s *Server) handleListConfigs(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.FromContext(r.Context())
	if !ok {
		http.Error(w, "unauthenticated", http.StatusUnauthorized)
		return
	}
	src, side, err := s.sourceFor(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	entries, err := src.List(r.Context())
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	out := make([]configEntry, 0, len(entries))
	for _, e := range entries {
		if !s.authz.AllowedConfig(user.Roles, e.Path, OpConfigRead) {
			continue
		}
		out = append(out, configEntry{
			Path:     e.Path,
			Writable: s.authz.AllowedConfig(user.Roles, e.Path, OpConfigWrite),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"entries": out,
		// An empty list has two quite different causes, and the page should not
		// have to guess which. "Your roles cover nothing" is a matter for
		// Access management; "there is nothing under the root" is a matter for
		// the deployment, and naming the root is what makes that actionable
		// rather than mysterious.
		"granted": len(s.authz.VisibleConfigPaths(user.Roles)) > 0,
		"root":    src.Root(),
		// Which system a change goes to should never be a guess, least of all
		// where writing to the wrong one is undone by a synchroniser later.
		"kind": s.configs.Kind(),
		// Empty unless both are configured. Present, it lets the page offer the
		// other side and say which one is being looked at.
		"sides": s.sides(),
		"side":  side,
	})
}

// sides lists the sides of a paired deployment, or nothing for a single source.
func (s *Server) sides() []string {
	if d, ok := s.configs.(configsource.Dual); ok {
		return d.Sides()
	}
	return nil
}

// sourceFor resolves ?source= against a paired deployment.
//
// Without the parameter the primary side is used, which is the repository: it
// decides what should exist, and is where an ordinary save goes.
func (s *Server) sourceFor(r *http.Request) (configsource.Source, string, error) {
	d, ok := s.configs.(configsource.Dual)
	if !ok {
		return s.configs, "", nil
	}
	want := r.URL.Query().Get("source")
	if want == "" {
		return s.configs, d.Sides()[0], nil
	}
	src, ok := d.Side(want)
	if !ok {
		return nil, "", fmt.Errorf("unknown source %q", want)
	}
	return src, want, nil
}

// handleGetConfig returns one entry's content.
func (s *Server) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	user, path, ok := s.configRequest(w, r, OpConfigRead)
	if !ok {
		return
	}
	src, side, err := s.sourceFor(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	var (
		f          configsource.File
		driftState string
	)
	// With two sides, one call answers both questions the page asks — what is
	// in the file, and whether the sides agree — reading each side once and at
	// the same time. Asking separately read the file twice over.
	if d, ok := s.configs.(configsource.Dual); ok {
		var dr configsource.Drift
		f, dr, err = d.GetWithDrift(r.Context(), side, path)
		driftState = dr.State
	} else {
		f, err = src.Get(r.Context(), path)
	}
	if errors.Is(err, configsource.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such configuration entry"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	out := map[string]any{
		"path":      f.Path,
		"content":   f.Content,
		"version":   f.Version,
		"author":    f.Author,
		"updatedAt": f.UpdatedAt,
		"side":      side,
		"writable":  s.authz.AllowedConfig(user.Roles, path, OpConfigWrite),
	}
	// How the two sides stand, for this file only. Comparing every file in the
	// listing would mean reading every one of them from both systems on every
	// visit; comparing the one on screen comes free with opening it.
	if driftState != "" {
		out["drift"] = driftState
	}
	writeJSON(w, http.StatusOK, out)
}

type saveConfigRequest struct {
	Content string `json:"content"`
	// Version is what the editor opened. Sending it back is what makes the save
	// refuse to overwrite somebody else's change; empty means the entry is
	// expected not to exist yet.
	Version string `json:"version"`
}

// handleSaveConfig writes one entry, provided nobody else changed it first.
func (s *Server) handleSaveConfig(w http.ResponseWriter, r *http.Request) {
	user, path, ok := s.configRequest(w, r, OpConfigWrite)
	if !ok {
		return
	}
	var body saveConfigRequest
	if err := decodeJSON(w, r, &body); err != nil {
		return
	}

	editor := configsource.Editor{Username: user.Username, Email: user.Email}
	target := s.configs.Kind() + ":" + path

	var err error
	if side := r.URL.Query().Get("source"); side != "" {
		d, ok := s.configs.(configsource.Dual)
		if !ok {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error": "this instance has one configuration source; there is no other side to write",
			})
			return
		}
		// The emergency path: Consul alone, leaving the repository behind. It is
		// recorded as its own operation so that it stands out in the audit trail
		// — the point of it is that it deliberately creates a difference.
		err = d.PutSide(r.Context(), side, path, body.Content, editor)
		target = "consul-only:" + path
	} else {
		// What Consul held before being overwritten. Described, never quoted:
		// these files carry database and mail passwords, and an audit trail
		// that reproduces them turns every reader of the log into someone who
		// has seen every credential in the estate.
		if d, ok := s.configs.(configsource.Dual); ok {
			target += " (consul was " + d.SecondaryState(r.Context(), path, body.Content) + ")"
		}
		err = s.configs.Put(r.Context(), path, body.Content, body.Version, editor)
	}

	// Recorded whether it succeeded or not: a change to what a service runs
	// with is exactly what an incident review looks for afterwards.
	s.audit.Log(audit.Entry{
		User: user.Username, Environment: s.cfg.Environment, Namespace: "-",
		Operation: OpConfigWrite, Target: target,
		Allowed: true, Success: err == nil, Error: errStr(err),
	})

	switch {
	case errors.Is(err, configsource.ErrConflict):
		// 409 rather than 500: nothing is broken, the editor is simply out of
		// date and the browser can say so and offer to reload.
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
	case errors.Is(err, configsource.ErrSecondaryLagging):
		// The commit landed; only the copy did not. Reporting this as a failure
		// would invite a second save of a change that is already committed.
		writeJSON(w, http.StatusOK, map[string]string{
			"status": "saved",
			"warning": "Committed, but Consul was not updated directly, so the services still " +
				"have the old values. The synchroniser will bring it across. (" + err.Error() + ")",
		})
	case err != nil:
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
	default:
		writeJSON(w, http.StatusOK, map[string]string{"status": "saved"})
	}
}

// configRequest resolves the caller and the requested path, and checks the
// permission for it. The path arrives as a query parameter rather than a URL
// segment because it contains slashes.
func (s *Server) configRequest(w http.ResponseWriter, r *http.Request, op string) (*auth.User, string, bool) {
	user, ok := auth.FromContext(r.Context())
	if !ok {
		http.Error(w, "unauthenticated", http.StatusUnauthorized)
		return nil, "", false
	}
	path, err := cleanConfigPath(r.URL.Query().Get("path"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return nil, "", false
	}
	if !s.authz.AllowedConfig(user.Roles, path, op) {
		s.audit.Log(audit.Entry{
			User: user.Username, Environment: s.cfg.Environment, Namespace: "-",
			Operation: op, Target: path, Allowed: false, Success: false,
		})
		http.Error(w, "forbidden", http.StatusForbidden)
		return nil, "", false
	}
	return user, path, true
}

// cleanConfigPath rejects anything that would address an entry outside the root.
//
// The root is prepended to whatever arrives here, so a path containing ".." or
// a leading slash could be made to name an entry elsewhere in the store or the
// repository. The grant check runs on this cleaned form, so it must be the same
// string that is eventually sent on.
func cleanConfigPath(p string) (string, error) {
	p = strings.Trim(p, "/")
	if p == "" {
		return "", errors.New("path is required")
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return "", errors.New("path is not a valid configuration path")
		}
	}
	return p, nil
}

// requireConfigs answers 404 when no configuration source is switched on, so
// that an installation without the feature has no endpoint at all rather than
// one that fails obscurely.
func (s *Server) requireConfigs(handler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.configs == nil {
			writeJSON(w, http.StatusNotFound, map[string]string{
				"error": "configuration management is not enabled on this instance",
			})
			return
		}
		handler(w, r)
	}
}
