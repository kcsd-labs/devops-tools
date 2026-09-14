package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"testing"

	"devops-tools/internal/config"
)

// raceBackend loses the first write the way a second replica would: by the time
// the store saves, somebody else has already written something else.
type raceBackend struct {
	doc     []byte
	version int
	// other is what the competing writer leaves behind, applied on the first
	// Save. Nil means no race.
	other []byte
	saves int
}

func (b *raceBackend) Describe() string { return "fake" }

func (b *raceBackend) Watch(context.Context, func([]byte, string)) {}

// ver is empty while nothing is stored, which is the version a first write
// expects — the same contract the file backend keeps for a file that is not
// there yet.
func (b *raceBackend) ver() string {
	if b.doc == nil {
		return ""
	}
	return fmt.Sprintf("v%d", b.version)
}

func (b *raceBackend) Load(context.Context) ([]byte, string, error) {
	if b.doc == nil {
		return nil, "", fs.ErrNotExist
	}
	return b.doc, b.ver(), nil
}

func (b *raceBackend) Save(_ context.Context, data []byte, version string) (string, error) {
	b.saves++
	if b.other != nil {
		b.doc, b.other = b.other, nil
		b.version++
		return "", ErrConflict
	}
	if version != b.ver() {
		return "", ErrConflict
	}
	b.doc = data
	b.version++
	return b.ver(), nil
}

func doc(t *testing.T, users map[string]*User) []byte {
	t.Helper()
	data, err := json.Marshal(encodeFile(users, nil, nil))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestAConflictReappliesTheChange(t *testing.T) {
	// The store holds one person. While it is deciding, another replica records
	// a second one. The change must land on top of that, not erase it.
	b := &raceBackend{
		doc:   doc(t, map[string]*User{"anna": {Username: "anna"}}),
		other: doc(t, map[string]*User{"anna": {Username: "anna"}, "boris": {Username: "boris"}}),
	}
	s, err := OpenWith(b)
	if err != nil {
		t.Fatal(err)
	}

	if err := s.SetRoles("anna", []string{"payments-developer"}); err != nil {
		t.Fatal(err)
	}

	var stored file
	if err := json.Unmarshal(b.doc, &stored); err != nil {
		t.Fatal(err)
	}
	if got := stored.Users["anna"].Roles; len(got) != 1 || got[0] != "payments-developer" {
		t.Errorf("the change was lost: anna has %v", got)
	}
	if _, ok := stored.Users["boris"]; !ok {
		t.Error("the other writer's record was overwritten — this is the failure with no error")
	}
	if b.saves != 2 {
		t.Errorf("saves=%d, want two: the first refused, the second applied again", b.saves)
	}
}

func TestSightingsAreWrittenInOneBatch(t *testing.T) {
	b := &raceBackend{doc: doc(t, map[string]*User{})}
	s, err := OpenWith(b)
	if err != nil {
		t.Fatal(err)
	}
	seen := func() {
		t.Helper()
		if err := s.RecordSeen("anna", "anna@example.com", "ldap", nil, nil); err != nil {
			t.Fatal(err)
		}
	}

	seen() // the first sighting is a new record, so it is written
	before := b.saves

	// A token provider reaches this on every single request. None of them says
	// anything new but the time, so none of them writes.
	for i := 0; i < 20; i++ {
		seen()
	}
	if b.saves != before {
		t.Fatalf("saves went from %d to %d; only the timestamp moved", before, b.saves)
	}

	// The time is in memory, so the interface shows it straight away.
	if u, ok := s.Get("anna"); !ok || u.LastSeen.IsZero() {
		t.Fatal("the sighting was not recorded in memory")
	}

	if err := s.FlushSeen(); err != nil {
		t.Fatal(err)
	}
	if b.saves != before+1 {
		t.Fatalf("the flush wrote %d times, want one", b.saves-before)
	}

	// Nothing pending now, so nothing to write.
	if err := s.FlushSeen(); err != nil {
		t.Fatal(err)
	}
	if b.saves != before+1 {
		t.Fatalf("a flush with nothing pending wrote anyway")
	}
}

func TestAnEmptyBackendIsWrittenAtStartup(t *testing.T) {
	// So that somewhere unwritable is reported now rather than at the first
	// sign-in, hours later.
	b := &raceBackend{}
	if _, err := OpenWith(b); err != nil {
		t.Fatal(err)
	}
	if b.doc == nil {
		t.Fatal("an empty store was not written")
	}
}

func TestAFileStoreStillReadsWhatItWrote(t *testing.T) {
	path := t.TempDir() + "/access.json"
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Invite("anna", "anna@example.com", []string{"readonly"}); err != nil {
		t.Fatal(err)
	}
	again, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := again.RolesFor("anna"); len(got) != 1 || got[0] != "readonly" {
		t.Fatalf("reopened store has %v", got)
	}
}

func TestAFileChangedUnderneathIsRefusedRatherThanOverwritten(t *testing.T) {
	path := t.TempDir() + "/access.json"
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	// A second process — or somebody with an editor — writes the file.
	other, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := other.Invite("boris", "", nil); err != nil {
		t.Fatal(err)
	}
	// The first store still holds the version from before, so its write is
	// refused, reloaded and applied again.
	if err := s.Invite("anna", "", nil); err != nil && !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	again, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"anna", "boris"} {
		if _, ok := again.Get(name); !ok {
			t.Errorf("%s is missing: one writer overwrote the other", name)
		}
	}
}

func TestASnapshotGoesBackIn(t *testing.T) {
	b := &raceBackend{doc: doc(t, map[string]*User{})}
	s, err := OpenWith(b)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Invite("anna", "", []string{"readonly"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveRole("readonly", config.RoleSpec{Description: "Look, do not touch"}); err != nil {
		t.Fatal(err)
	}

	snapshot, err := s.Snapshot()
	if err != nil {
		t.Fatal(err)
	}

	// Somebody makes a mess afterwards.
	if err := s.DeleteUser("anna"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeleteRole("readonly"); err != nil {
		t.Fatal(err)
	}
	if len(s.List()) != 0 || len(s.ListRoles()) != 0 {
		t.Fatal("the mess was not made")
	}

	if err := s.Restore(snapshot); err != nil {
		t.Fatal(err)
	}
	if got := s.RolesFor("anna"); len(got) != 1 || got[0] != "readonly" {
		t.Fatalf("after restoring, anna has %v", got)
	}
	if !s.RoleExists("readonly") {
		t.Fatal("the role did not come back")
	}

	// And it is stored, not only in memory.
	again, err := OpenWith(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(again.List()) != 1 {
		t.Fatal("the restored model was not written")
	}
}

func TestARestoreThatIsNotAnAccessModelIsRefused(t *testing.T) {
	b := &raceBackend{doc: doc(t, map[string]*User{"anna": {Username: "anna"}})}
	s, err := OpenWith(b)
	if err != nil {
		t.Fatal(err)
	}

	cases := map[string]string{
		"not JSON at all":          `certainly not`,
		"JSON, but not this":       `{"hello":"world"}`,
		"written by a later build": `{"version":99,"users":{}}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if err := s.Restore([]byte(body)); err == nil {
				t.Fatal("accepted")
			}
			// And nothing was lost trying.
			if _, ok := s.Get("anna"); !ok {
				t.Fatal("a refused restore emptied the store")
			}
		})
	}
}
