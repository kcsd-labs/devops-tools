package store

import (
	"fmt"
	"sort"

	"devops-tools/internal/config"
)

// SeedRoles copies a deployed access model into an empty store.
//
// It runs once, on a store that has no roles yet. After that the roles are
// edited here and the deployed configuration is not consulted again — otherwise
// every `helm upgrade` would quietly undo whatever was set up in the UI, which
// is the worst kind of failure: no error, and the access model is back to
// whatever the chart happened to say.
//
// Reports whether it seeded, so the caller can say which of the two is in
// force rather than leaving an operator to guess.
func (s *Store) SeedRoles(cfg *config.RBACConfig) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.roles) > 0 {
		return false, nil
	}
	for name, spec := range cfg.Roles {
		s.roles[name] = spec
	}
	return true, s.save()
}

// AccessModel builds the model the authorizer answers from. Operations come
// from the deployment: they are the vocabulary the code understands, not
// something an administrator invents.
func (s *Store) AccessModel(operations []string) *config.RBACConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()

	roles := make(map[string]config.RoleSpec, len(s.roles))
	for k, v := range s.roles {
		roles[k] = v
	}
	return &config.RBACConfig{Operations: operations, Roles: roles}
}

// NamedRole is one role with its name, for listing.
type NamedRole struct {
	Name string `json:"name"`
	config.RoleSpec
}

// ListRoles returns every role, ordered by name.
func (s *Store) ListRoles() []NamedRole {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]NamedRole, 0, len(s.roles))
	for name, spec := range s.roles {
		out = append(out, NamedRole{Name: name, RoleSpec: spec})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// SaveRole creates or replaces a role.
func (s *Store) SaveRole(name string, spec config.RoleSpec) error {
	if err := ValidateUsername(name); err != nil {
		return fmt.Errorf("role name: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.roles[name] = spec
	return s.save()
}

// DeleteRole removes a role, and with it every grant it carried.
//
// Users holding it keep the name in their list — harmless, since a role that
// does not exist grants nothing — but the caller is told who they are, so an
// administrator finds out now rather than when someone reports losing access.
func (s *Store) DeleteRole(name string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.roles[name]; !ok {
		return nil, fmt.Errorf("role %q: %w", name, ErrNotFound)
	}
	delete(s.roles, name)

	var affected []string
	for _, u := range s.users {
		for _, r := range u.Roles {
			if r == name {
				affected = append(affected, u.Username)
				break
			}
		}
	}
	sort.Strings(affected)
	return affected, s.save()
}

// Membership is the outcome of changing who holds a role.
type Membership struct {
	// Changed are the users whose stored roles actually moved.
	Changed []string
	// Unchanged already had it, or already did not — worth reporting so that
	// "3 of 5" does not read as a failure.
	Unchanged []string
	// Unknown are names with no record here. Under a directory provider people
	// appear only once they have signed in, so this is the ordinary way a name
	// is not found rather than a fault.
	Unknown []string
}

// ChangeRoleMembership adds or removes one role across many users, in one write.
//
// One role, never a replacement of somebody's whole set: a bulk action that
// replaced every selected person's roles would have no safe outcome if the
// wrong people were selected. Adding and removing one named role is undoable by
// doing the opposite.
//
// The whole batch is one lock and one save, so it cannot land half applied —
// which a loop of single-user calls from the browser could.
func (s *Store) ChangeRoleMembership(role string, usernames []string, add bool) (Membership, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.roles[role]; !ok {
		return Membership{}, fmt.Errorf("role %q: %w", role, ErrNotFound)
	}

	var out Membership
	for _, name := range usernames {
		u, ok := s.users[key(name)]
		if !ok {
			out.Unknown = append(out.Unknown, name)
			continue
		}
		had := false
		kept := make([]string, 0, len(u.Roles)+1)
		for _, r := range u.Roles {
			if r == role {
				had = true
				continue
			}
			kept = append(kept, r)
		}
		if add == had {
			out.Unchanged = append(out.Unchanged, u.Username)
			continue
		}
		if add {
			kept = append(kept, role)
		}
		sort.Strings(kept)
		u.Roles = kept
		out.Changed = append(out.Changed, u.Username)
	}
	sort.Strings(out.Changed)
	sort.Strings(out.Unchanged)
	sort.Strings(out.Unknown)

	if len(out.Changed) == 0 {
		// Nothing moved, so nothing to write.
		return out, nil
	}
	return out, s.save()
}

// RoleHolders counts who holds each role, so the roles list can say how many
// people a role reaches without anyone scanning the users table by eye.
//
// Only what is stored. A role granted through auth.bootstrapAdmins is added on
// top at request time and is not counted here — see the note where that grant
// is resolved.
func (s *Store) RoleHolders() map[string]int {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make(map[string]int, len(s.roles))
	for name := range s.roles {
		out[name] = 0
	}
	for _, u := range s.users {
		for _, r := range u.Roles {
			if _, ok := s.roles[r]; ok {
				out[r]++
			}
		}
	}
	return out
}

// RoleExists reports whether a role is defined, so that assigning a name nobody
// spelled correctly fails loudly instead of granting nothing.
func (s *Store) RoleExists(name string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.roles[name]
	return ok
}
