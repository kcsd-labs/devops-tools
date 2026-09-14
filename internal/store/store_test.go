package store

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func open(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sub", "devops-tools.json")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	return s, path
}

// The file has to appear at startup, not at the first write: that is what turns
// an unwritable volume into a startup failure instead of a surprise later.
func TestOpenCreatesTheFile(t *testing.T) {
	_, path := open(t)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected the store file to exist: %v", err)
	}
}

func TestRecordSeenCreatesAndKeepsRoles(t *testing.T) {
	s, path := open(t)

	if err := s.RecordSeen("Ivan", "ivan@example.com", "ldap", []string{"devs"}, []string{"developer"}); err != nil {
		t.Fatal(err)
	}
	if got := s.RolesFor("Ivan"); len(got) != 1 || got[0] != "developer" {
		t.Fatalf("initial roles = %v", got)
	}

	// Signing in again must not re-apply the provider's roles. Otherwise a role
	// taken away in the portal would come straight back at the next sign-in.
	if err := s.RecordSeen("Ivan", "ivan@example.com", "ldap", []string{"devs"}, []string{"platform-admin"}); err != nil {
		t.Fatal(err)
	}
	if got := s.RolesFor("Ivan"); len(got) != 1 || got[0] != "developer" {
		t.Fatalf("roles after the second sign-in = %v, want the stored ones", got)
	}

	// And it survives a restart.
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := s2.RolesFor("Ivan"); len(got) != 1 || got[0] != "developer" {
		t.Fatalf("roles after reopening = %v", got)
	}
}

// Directories are rarely case-sensitive; the same person must not become two
// records — and worse, one of them without the roles they were granted.
func TestUsernameIsCaseInsensitive(t *testing.T) {
	s, _ := open(t)
	if err := s.RecordSeen("Ivan", "", "ldap", nil, []string{"developer"}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordSeen("ivan", "", "ldap", nil, []string{"platform-admin"}); err != nil {
		t.Fatal(err)
	}
	if n := s.Count(); n != 1 {
		t.Fatalf("expected one user, got %d", n)
	}
	if got := s.RolesFor("IVAN"); len(got) != 1 || got[0] != "developer" {
		t.Fatalf("roles = %v", got)
	}
}

// An unknown user has no roles — the point of default deny.
func TestUnknownUserHasNoRoles(t *testing.T) {
	s, _ := open(t)
	if got := s.RolesFor("nobody"); len(got) != 0 {
		t.Fatalf("expected no roles, got %v", got)
	}
}

// A caller must not be able to change the stored roles through the slice it
// was handed.
func TestRolesForReturnsACopy(t *testing.T) {
	s, _ := open(t)
	if err := s.RecordSeen("ivan", "", "local", nil, []string{"developer"}); err != nil {
		t.Fatal(err)
	}
	got := s.RolesFor("ivan")
	got[0] = "platform-admin"
	if again := s.RolesFor("ivan"); again[0] != "developer" {
		t.Fatalf("the stored roles were modified through the returned slice: %v", again)
	}
}

func TestGroupsAreRefreshedButRolesAreNot(t *testing.T) {
	s, _ := open(t)
	if err := s.RecordSeen("ivan", "", "ldap", []string{"devs"}, []string{"developer"}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordSeen("ivan", "", "ldap", []string{"devs", "oncall"}, nil); err != nil {
		t.Fatal(err)
	}
	if got := s.GroupsFor("ivan"); len(got) != 2 {
		t.Fatalf("groups = %v, want the refreshed pair", got)
	}
	if got := s.RolesFor("ivan"); len(got) != 1 {
		t.Fatalf("roles = %v, want them untouched", got)
	}
}

// Writing must not be able to destroy what is already there. The rename is what
// guarantees it; this checks the file is still valid and complete afterwards.
func TestListSurvivesRepeatedWrites(t *testing.T) {
	s, path := open(t)
	for _, n := range []string{"a", "b", "c"} {
		if err := s.RecordSeen(n, "", "local", nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := s2.List(); len(got) != 3 {
		t.Fatalf("reopened with %d users, want 3", len(got))
	}
	// Sorted by username, so the order is stable for the UI.
	if got := s2.List(); got[0].Username != "a" || got[2].Username != "c" {
		t.Fatalf("unexpected order: %v", got)
	}
}

func TestFirstSeenIsNotOverwritten(t *testing.T) {
	s, _ := open(t)
	if err := s.RecordSeen("ivan", "", "local", nil, nil); err != nil {
		t.Fatal(err)
	}
	first := s.List()[0].FirstSeen
	if first.IsZero() {
		t.Fatal("firstSeen was not set")
	}
	time.Sleep(2 * time.Millisecond)
	if err := s.RecordSeen("ivan", "changed@example.com", "local", nil, nil); err != nil {
		t.Fatal(err)
	}
	if got := s.List()[0].FirstSeen; !got.Equal(first) {
		t.Fatalf("firstSeen changed from %v to %v", first, got)
	}
}

// A store written by a newer build must not be silently reinterpreted.
func TestRefusesANewerFormat(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "devops-tools.json")
	if err := os.WriteFile(path, []byte(`{"version":99,"users":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); err == nil {
		t.Fatal("expected a newer format to be refused")
	}
}

func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o600)
}

func TestGroupNamesAreStoredOnce(t *testing.T) {
	// The same team, written out once per member, is what made the document ten
	// times larger than it needed to be.
	team := []string{
		"CN=Infrastructure,OU=Groups,OU=Departments,DC=corp,DC=example,DC=com",
		"CN=Payments,OU=Groups,OU=Departments,DC=corp,DC=example,DC=com",
	}
	users := map[string]*User{}
	for i := 0; i < 50; i++ {
		name := fmt.Sprintf("user%02d", i)
		users[name] = &User{Username: name, Provider: "ldap", Groups: team}
	}

	f := encodeFile(users, nil, nil)
	if len(f.Groups) != 2 {
		t.Fatalf("the table holds %d names, want the two distinct ones", len(f.Groups))
	}
	for _, u := range f.Users {
		if len(u.Groups) != 0 {
			t.Fatal("a user still carries the names themselves")
		}
		if len(u.GroupIDs) != 2 {
			t.Fatalf("a user has %d indices, want 2", len(u.GroupIDs))
		}
	}
}

func TestTheSameModelEncodesToTheSameBytes(t *testing.T) {
	// Otherwise every write would look like a change to whoever is watching the
	// object, and two replicas would chase each other.
	users := map[string]*User{
		"anna":  {Username: "anna", Groups: []string{"CN=b,DC=x", "CN=a,DC=x"}},
		"boris": {Username: "boris", Groups: []string{"CN=a,DC=x"}},
	}
	first, err := json.Marshal(encodeFile(users, nil, nil))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		again, err := json.Marshal(encodeFile(users, nil, nil))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(first, again) {
			t.Fatal("encoding the same model twice gave different bytes")
		}
	}
}

func TestAStoreWrittenByTheOlderFormatStillOpens(t *testing.T) {
	// Version 2 wrote the group names into each user. An upgrade must read that
	// and carry it across, not lose everybody's context.
	path := t.TempDir() + "/access.json"
	old := `{"version":2,"users":{"anna":{"username":"anna","provider":"ldap",` +
		`"groups":["CN=Payments,DC=x","CN=Infrastructure,DC=x"],"roles":["readonly"],` +
		`"firstSeen":"2026-01-01T00:00:00Z","lastSeen":"2026-01-02T00:00:00Z"}}}`
	if err := os.WriteFile(path, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.GroupsFor("anna"); len(got) != 2 {
		t.Fatalf("groups after reading the older format: %v", got)
	}

	// And the next write puts it in the current format.
	if err := s.SetRoles("anna", []string{"platform-admin"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var f file
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatal(err)
	}
	if f.Version != currentVersion || len(f.Groups) != 2 {
		t.Fatalf("rewritten as version %d with %d group names", f.Version, len(f.Groups))
	}
	if got := s.GroupsFor("anna"); len(got) != 2 {
		t.Fatalf("groups were lost in the rewrite: %v", got)
	}
}
