package store

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"devops-tools/internal/config"
)

func TestCreateLocalUserAndSignIn(t *testing.T) {
	s, path := open(t)

	if err := s.CreateLocalUser("ivan", "ivan@example.com", "hash", []string{"developer"}); err != nil {
		t.Fatal(err)
	}
	hash, ok := s.LocalCredential("ivan")
	if !ok || hash != "hash" {
		t.Fatalf("credential = %q, %v", hash, ok)
	}
	// Case-insensitively, like every other username lookup.
	if _, ok := s.LocalCredential("IVAN"); !ok {
		t.Fatal("credential lookup is case-sensitive")
	}
	// And it survives a restart, otherwise the account would exist only until
	// the pod was rescheduled.
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s2.LocalCredential("ivan"); !ok {
		t.Fatal("the account did not survive reopening")
	}
}

func TestCreateLocalUserRejectsDuplicates(t *testing.T) {
	s, _ := open(t)
	if err := s.CreateLocalUser("ivan", "", "hash", nil); err != nil {
		t.Fatal(err)
	}
	err := s.CreateLocalUser("ivan", "", "other", nil)
	if !errors.Is(err, ErrExists) {
		t.Fatalf("expected ErrExists, got %v", err)
	}
	// The original password must not have been replaced by the failed attempt.
	if hash, _ := s.LocalCredential("ivan"); hash != "hash" {
		t.Fatalf("password changed to %q by a rejected create", hash)
	}
}

// An account that came from a directory, or from the configuration file, has
// its password elsewhere. Setting one here would create a second way in that
// nobody expects to exist.
func TestPasswordOnlyForManagedAccounts(t *testing.T) {
	s, _ := open(t)
	if err := s.RecordSeen("ivan", "", "ldap", nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPassword("ivan", "hash"); !errors.Is(err, ErrNotManaged) {
		t.Fatalf("expected ErrNotManaged, got %v", err)
	}
	if _, ok := s.LocalCredential("ivan"); ok {
		t.Fatal("a directory account must have no local credential")
	}
	// Deleting one is allowed, and is a different matter: it takes the roles
	// away rather than creating a way in. See the deletion test.
}

func TestSetRolesAndDelete(t *testing.T) {
	s, _ := open(t)
	if err := s.CreateLocalUser("ivan", "", "hash", []string{"developer"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetRoles("ivan", []string{"viewer", "developer"}); err != nil {
		t.Fatal(err)
	}
	if got := s.RolesFor("ivan"); len(got) != 2 {
		t.Fatalf("roles = %v", got)
	}
	// Revoking everything must leave nothing behind.
	if err := s.SetRoles("ivan", nil); err != nil {
		t.Fatal(err)
	}
	if got := s.RolesFor("ivan"); len(got) != 0 {
		t.Fatalf("roles after revoking = %v", got)
	}
	if err := s.DeleteUser("ivan"); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Get("ivan"); ok {
		t.Fatal("the account is still there after deletion")
	}
}

func TestValidateUsername(t *testing.T) {
	ok := []string{"ivan", "ivan.petrov", "a1", "user-name_2"}
	bad := []string{"", "a", "-leading", "with space", "sla/sh", "..", "ünïcode",
		"averyveryveryveryveryveryveryveryveryveryveryverylongusername-that-is-too-long"}
	for _, n := range ok {
		if err := ValidateUsername(n); err != nil {
			t.Errorf("expected %q to be accepted: %v", n, err)
		}
	}
	for _, n := range bad {
		if err := ValidateUsername(n); err == nil {
			t.Errorf("expected %q to be rejected", n)
		}
	}
}

func TestNormaliseRoles(t *testing.T) {
	got := NormaliseRoles([]string{" developer ", "developer", "", "  ", "viewer"})
	if len(got) != 2 || got[0] != "developer" || got[1] != "viewer" {
		t.Fatalf("got %v", got)
	}
	// Never nil, so it serialises as [] rather than null.
	if NormaliseRoles(nil) == nil {
		t.Fatal("expected an empty slice, not nil")
	}
}

// --- roles -----------------------------------------------------------------

func seedCfg() *config.RBACConfig {
	return &config.RBACConfig{
		Operations: []string{"logs", "secret-list", "user-list"},
		Roles: map[string]config.RoleSpec{
			"platform-admin": {
				Description: "Everything",
				Namespaces:  []config.NamespaceGrant{{Namespace: "*", Operations: []string{"*"}}},
				Global:      []string{"user-list"},
			},
		},
	}
}

// Seeding happens once. Running it again — every restart does — must not undo
// what an administrator changed in the meantime.
func TestSeedRolesOnlyOnce(t *testing.T) {
	s, path := open(t)

	seeded, err := s.SeedRoles(seedCfg())
	if err != nil || !seeded {
		t.Fatalf("first seed: seeded=%v err=%v", seeded, err)
	}
	if err := s.SaveRole("developer", config.RoleSpec{
		Namespaces: []config.NamespaceGrant{{Namespace: "payments-dev", Operations: []string{"logs"}}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeleteRole("platform-admin"); err != nil {
		t.Fatalf("delete: %v", err)
	}

	// Restart, then seed again as the service does.
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	seeded, err = s2.SeedRoles(seedCfg())
	if err != nil {
		t.Fatal(err)
	}
	if seeded {
		t.Fatal("a second seed ran and overwrote the stored model")
	}
	names := s2.ListRoles()
	if len(names) != 1 || names[0].Name != "developer" {
		t.Fatalf("roles after restart = %v, want only the one that was kept", names)
	}
}

func TestAccessModelCarriesTheDeploymentsOperations(t *testing.T) {
	s, _ := open(t)
	if _, err := s.SeedRoles(seedCfg()); err != nil {
		t.Fatal(err)
	}
	m := s.AccessModel([]string{"logs", "metrics"})
	if len(m.Operations) != 2 {
		t.Fatalf("operations = %v", m.Operations)
	}
	if _, ok := m.Roles["platform-admin"]; !ok {
		t.Fatalf("roles = %v", m.Roles)
	}
	// A copy: editing the returned model must not reach into the store.
	delete(m.Roles, "platform-admin")
	if !s.RoleExists("platform-admin") {
		t.Fatal("the stored role was removed through the returned model")
	}
}

// Deleting a role has to say who was holding it — otherwise the loss of access
// is discovered by the person who lost it.
func TestDeleteRoleReportsAffectedUsers(t *testing.T) {
	s, _ := open(t)
	if _, err := s.SeedRoles(seedCfg()); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateLocalUser("ivan", "", "h", []string{"platform-admin"}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateLocalUser("anna", "", "h", []string{"platform-admin"}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateLocalUser("boris", "", "h", nil); err != nil {
		t.Fatal(err)
	}
	affected, err := s.DeleteRole("platform-admin")
	if err != nil {
		t.Fatal(err)
	}
	if len(affected) != 2 || affected[0] != "anna" || affected[1] != "ivan" {
		t.Fatalf("affected = %v", affected)
	}
	if s.RoleExists("platform-admin") {
		t.Fatal("the role is still defined")
	}
}

func TestDeleteUnknownRole(t *testing.T) {
	s, _ := open(t)
	if _, err := s.DeleteRole("nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

// A store written before roles existed must keep its users when opened by a
// build that has them.
func TestOpensAVersionOneFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "devops-tools.json")
	old := `{"version":1,"users":{"ivan":{"username":"ivan","provider":"ldap","roles":["developer"],
	  "firstSeen":"2026-01-01T00:00:00Z","lastSeen":"2026-01-01T00:00:00Z"}}}`
	if err := writeFile(path, old); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatalf("opening a version 1 store: %v", err)
	}
	if got := s.RolesFor("ivan"); len(got) != 1 || got[0] != "developer" {
		t.Fatalf("roles = %v", got)
	}
	if len(s.ListRoles()) != 0 {
		t.Fatal("a version 1 store should start with no roles, ready to be seeded")
	}
}

// --- bulk role membership --------------------------------------------------

func withUsers(t *testing.T, roles []string, users map[string][]string) *Store {
	t.Helper()
	s, _ := open(t)
	spec := map[string]config.RoleSpec{}
	for _, r := range roles {
		spec[r] = config.RoleSpec{Description: r}
	}
	if _, err := s.SeedRoles(&config.RBACConfig{Roles: spec}); err != nil {
		t.Fatalf("SeedRoles: %v", err)
	}
	for name, rs := range users {
		if err := s.RecordSeen(name, name+"@example.com", "oidc", nil, rs); err != nil {
			t.Fatalf("RecordSeen %s: %v", name, err)
		}
	}
	return s
}

// The point of the batch: several people, one write. A loop of single-user
// calls can fail half way and leave the model in a state nobody chose.
func TestGrantingARoleToSeveralPeopleAtOnce(t *testing.T) {
	s := withUsers(t, []string{"abs-team", "viewer"}, map[string][]string{
		"ada":   {"viewer"},
		"grace": nil,
		"alan":  {"abs-team"}, // already has it
	})

	res, err := s.ChangeRoleMembership("abs-team", []string{"ada", "grace", "alan", "nobody"}, true)
	if err != nil {
		t.Fatalf("ChangeRoleMembership: %v", err)
	}
	if got := strings.Join(res.Changed, ","); got != "ada,grace" {
		t.Errorf("changed = %q, want ada,grace", got)
	}
	if got := strings.Join(res.Unchanged, ","); got != "alan" {
		t.Errorf("unchanged = %q, want alan — he already had it", got)
	}
	if got := strings.Join(res.Unknown, ","); got != "nobody" {
		t.Errorf("unknown = %q, want nobody", got)
	}
	// The roles people already had are untouched: this adds one, it does not
	// replace anybody's set.
	if got := strings.Join(s.RolesFor("ada"), ","); got != "abs-team,viewer" {
		t.Errorf("ada now holds %q; her other role was disturbed", got)
	}
}

func TestRevokingARoleLeavesTheOthersAlone(t *testing.T) {
	s := withUsers(t, []string{"abs-team", "viewer"}, map[string][]string{
		"ada": {"abs-team", "viewer"},
	})
	if _, err := s.ChangeRoleMembership("abs-team", []string{"ada"}, false); err != nil {
		t.Fatalf("ChangeRoleMembership: %v", err)
	}
	if got := strings.Join(s.RolesFor("ada"), ","); got != "viewer" {
		t.Errorf("ada holds %q, want only viewer", got)
	}
}

// A name that is not a role is refused outright rather than quietly writing a
// grant nobody can ever satisfy.
func TestUnknownRoleIsRefused(t *testing.T) {
	s := withUsers(t, []string{"viewer"}, map[string][]string{"ada": nil})
	if _, err := s.ChangeRoleMembership("does-not-exist", []string{"ada"}, true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if len(s.RolesFor("ada")) != 0 {
		t.Error("ada gained something from a refused call")
	}
}

// Everyone in the batch already being right is a normal outcome, not a failure,
// and it must not rewrite the file for nothing.
func TestNoChangesWritesNothing(t *testing.T) {
	s := withUsers(t, []string{"viewer"}, map[string][]string{"ada": {"viewer"}})
	res, err := s.ChangeRoleMembership("viewer", []string{"ada"}, true)
	if err != nil {
		t.Fatalf("ChangeRoleMembership: %v", err)
	}
	if len(res.Changed) != 0 || len(res.Unchanged) != 1 {
		t.Errorf("result = %+v, want nothing changed and one unchanged", res)
	}
}

// The count behind "who has this role", used so the roles list can say it
// without anyone reading the users table.
func TestRoleHoldersCountsOnlyRealRoles(t *testing.T) {
	s := withUsers(t, []string{"abs-team", "viewer"}, map[string][]string{
		"ada":   {"abs-team", "viewer"},
		"grace": {"abs-team"},
		// A role that was deleted after being granted leaves a name behind in
		// the user's list; it grants nothing and must not be counted.
		"alan": {"long-gone"},
	})
	h := s.RoleHolders()
	if h["abs-team"] != 2 || h["viewer"] != 1 {
		t.Errorf("holders = %v, want abs-team 2 and viewer 1", h)
	}
	if _, ok := h["long-gone"]; ok {
		t.Error("a role that no longer exists was counted")
	}
}

// --- invitations -----------------------------------------------------------

// The whole point: prepare access for somebody who has not signed in, and have
// it apply the moment they do. Under a directory provider there is otherwise
// nobody to grant anything to until they arrive.
func TestAnInvitationAppliesOnFirstSignIn(t *testing.T) {
	s := withUsers(t, []string{"abs-team"}, nil)

	if err := s.Invite("newcomer", "newcomer@example.com", []string{"abs-team"}); err != nil {
		t.Fatalf("Invite: %v", err)
	}
	u, ok := s.Get("newcomer")
	if !ok || !u.Invited() {
		t.Fatalf("record = %+v, %v; want an unclaimed invitation", u, ok)
	}
	if got := strings.Join(s.RolesFor("newcomer"), ","); got != "abs-team" {
		t.Errorf("roles before sign-in = %q; the grant should already be there", got)
	}

	// They arrive, under whichever provider — the invitation did not say.
	if err := s.RecordSeen("newcomer", "newcomer@corp", "oidc", []string{"devs"}, nil); err != nil {
		t.Fatalf("RecordSeen: %v", err)
	}
	u, _ = s.Get("newcomer")
	if u.Invited() {
		t.Error("still looks like an invitation after being claimed")
	}
	if got := strings.Join(u.Roles, ","); got != "abs-team" {
		t.Errorf("roles after sign-in = %q; the prepared grant was lost", got)
	}
	if u.Provider != "oidc" {
		t.Errorf("provider = %q, want the one they actually used", u.Provider)
	}
}

// Matching is case-insensitive everywhere else, and an invitation is no
// exception — otherwise "Newcomer" in the form and "newcomer" from the provider
// would quietly never meet.
func TestAnInvitationMatchesRegardlessOfCase(t *testing.T) {
	s := withUsers(t, []string{"abs-team"}, nil)
	if err := s.Invite("NewComer", "", []string{"abs-team"}); err != nil {
		t.Fatalf("Invite: %v", err)
	}
	if err := s.RecordSeen("newcomer", "", "oidc", nil, nil); err != nil {
		t.Fatalf("RecordSeen: %v", err)
	}
	if got := strings.Join(s.RolesFor("newcomer"), ","); got != "abs-team" {
		t.Errorf("roles = %q; the invitation did not match", got)
	}
	if n := len(s.List()); n != 1 {
		t.Errorf("%d records; the invitation and the arrival did not merge", n)
	}
}

// Inviting somebody who is already here is a mistake worth naming: their roles
// are not replaced behind their back.
func TestInvitingAnExistingUserIsRefused(t *testing.T) {
	s := withUsers(t, []string{"abs-team", "viewer"}, map[string][]string{"ada": {"viewer"}})
	if err := s.Invite("ada", "", []string{"abs-team"}); !errors.Is(err, ErrExists) {
		t.Fatalf("Invite err = %v, want ErrExists", err)
	}
	if got := strings.Join(s.RolesFor("ada"), ","); got != "viewer" {
		t.Errorf("ada now holds %q; a refused invitation changed her", got)
	}
}

// The usual reason to remove an invitation is that the name in it was wrong,
// so it has to be deletable — unlike the record of somebody who signed in,
// which would only come back.
func TestAnUnclaimedInvitationCanBeDeleted(t *testing.T) {
	s := withUsers(t, []string{"abs-team"}, nil)
	if err := s.Invite("typo", "", []string{"abs-team"}); err != nil {
		t.Fatalf("Invite: %v", err)
	}
	if err := s.DeleteUser("typo"); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
	if _, ok := s.Get("typo"); ok {
		t.Error("the invitation is still there")
	}
}

// Deleting the record of somebody who can still sign in is a revocation, not a
// removal: they come back on their next visit, with nothing. Worth pinning
// down, because it is what the confirmation promises.
func TestDeletingSomebodyWhoCanStillSignInRevokesTheirRoles(t *testing.T) {
	s := withUsers(t, []string{"abs-team"}, map[string][]string{"ada": {"abs-team"}})

	if err := s.DeleteUser("ada"); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
	if _, ok := s.Get("ada"); ok {
		t.Fatal("the record is still there")
	}

	if err := s.RecordSeen("ada", "", "oidc", nil, nil); err != nil {
		t.Fatalf("RecordSeen: %v", err)
	}
	u, ok := s.Get("ada")
	if !ok {
		t.Fatal("she did not come back on signing in")
	}
	if len(u.Roles) != 0 {
		t.Errorf("she came back holding %v; deletion should have taken the roles", u.Roles)
	}
}

// The claim must reach disk. Skipping that write leaves an invitation that is
// claimed again on every request — and, worse, one that still looks unclaimed
// to whoever is watching the list.
func TestTheClaimIsPersisted(t *testing.T) {
	s, path := open(t)
	if _, err := s.SeedRoles(&config.RBACConfig{
		Roles: map[string]config.RoleSpec{"abs-team": {}},
	}); err != nil {
		t.Fatalf("SeedRoles: %v", err)
	}
	if err := s.Invite("ada", "", []string{"abs-team"}); err != nil {
		t.Fatalf("Invite: %v", err)
	}
	if err := s.RecordSeen("ada", "", "oidc", nil, nil); err != nil {
		t.Fatalf("RecordSeen: %v", err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	u, ok := reopened.Get("ada")
	if !ok || u.Invited() {
		t.Errorf("after reopening: %+v, %v; the claim was not written", u, ok)
	}
}
