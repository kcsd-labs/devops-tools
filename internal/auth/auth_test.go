package auth

import (
	"sort"
	"testing"

	"devops-tools/internal/config"
)

// fakeDirectory stands in for the store.
type fakeDirectory struct {
	roles  map[string][]string
	groups map[string][]string
	seen   []string
}

func (f *fakeDirectory) RecordSeen(username, _, _ string, _, initialRoles []string) error {
	f.seen = append(f.seen, username)
	if _, ok := f.roles[username]; !ok && initialRoles != nil {
		f.roles[username] = initialRoles
	}
	return nil
}
func (f *fakeDirectory) RolesFor(u string) []string  { return f.roles[u] }
func (f *fakeDirectory) GroupsFor(u string) []string { return f.groups[u] }

func newDir(roles map[string][]string) *fakeDirectory {
	return &fakeDirectory{roles: roles, groups: map[string][]string{}}
}

func TestRolesForComesFromTheDirectory(t *testing.T) {
	s := &Service{dir: newDir(map[string][]string{"ivan": {"developer"}})}
	got := s.rolesFor("ivan")
	if len(got) != 1 || got[0] != "developer" {
		t.Fatalf("roles = %v", got)
	}
}

// Revoking in the portal has to take effect at once. This is the property the
// whole design exists for: nothing may keep a role alive after it is removed.
func TestRevokingLeavesNoRoles(t *testing.T) {
	dir := newDir(map[string][]string{"ivan": {"developer"}})
	s := &Service{dir: dir}

	delete(dir.roles, "ivan")

	if got := s.rolesFor("ivan"); len(got) != 0 {
		t.Fatalf("expected no roles after revocation, got %v", got)
	}
}

func TestBootstrapAdminAlwaysHoldsItsRoles(t *testing.T) {
	s := &Service{
		dir:       newDir(map[string][]string{}),
		bootstrap: config.BootstrapConfig{Users: []string{"admin"}, Roles: []string{"platform-admin"}},
	}
	got := s.rolesFor("admin")
	if len(got) != 1 || got[0] != "platform-admin" {
		t.Fatalf("bootstrap admin roles = %v", got)
	}
	// Case-insensitively, like every other username comparison.
	if got := s.rolesFor("ADMIN"); len(got) != 1 {
		t.Fatalf("bootstrap matching is case-sensitive: %v", got)
	}
	// And nobody else gets it.
	if got := s.rolesFor("ivan"); len(got) != 0 {
		t.Fatalf("bootstrap roles leaked to another user: %v", got)
	}
}

// The bootstrap grant adds to what is stored rather than replacing it, and must
// not produce the same role twice.
func TestBootstrapMergesWithStoredRoles(t *testing.T) {
	s := &Service{
		dir:       newDir(map[string][]string{"admin": {"developer", "platform-admin"}}),
		bootstrap: config.BootstrapConfig{Users: []string{"admin"}, Roles: []string{"platform-admin"}},
	}
	got := s.rolesFor("admin")
	sort.Strings(got)
	if len(got) != 2 || got[0] != "developer" || got[1] != "platform-admin" {
		t.Fatalf("roles = %v, want each exactly once", got)
	}
}

// Bootstrap without roles configured grants nothing, rather than everything.
func TestBootstrapWithoutRolesGrantsNothing(t *testing.T) {
	b := config.BootstrapConfig{Users: []string{"admin"}}
	if b.Grants("admin") {
		t.Fatal("a bootstrap entry with no roles should grant nothing")
	}
}

// The provider's roles seed a new user and are ignored thereafter — otherwise
// the directory would quietly undo every change made in the portal.
func TestProviderRolesOnlySeedANewUser(t *testing.T) {
	dir := newDir(map[string][]string{})
	s := &Service{kind: "ldap", dir: dir}

	s.register(&User{Username: "ivan", Roles: []string{"developer"}})
	if got := s.rolesFor("ivan"); len(got) != 1 || got[0] != "developer" {
		t.Fatalf("seeded roles = %v", got)
	}

	dir.roles["ivan"] = []string{"viewer"} // an administrator changes it
	s.register(&User{Username: "ivan", Roles: []string{"developer"}})
	if got := s.rolesFor("ivan"); len(got) != 1 || got[0] != "viewer" {
		t.Fatalf("the provider overwrote the stored roles: %v", got)
	}
}

// A Service built without a directory must not panic; it simply grants nothing
// beyond the bootstrap entry.
func TestNoDirectoryIsSafe(t *testing.T) {
	s := &Service{}
	if got := s.rolesFor("ivan"); len(got) != 0 {
		t.Fatalf("roles = %v", got)
	}
	s.register(&User{Username: "ivan"})
}
