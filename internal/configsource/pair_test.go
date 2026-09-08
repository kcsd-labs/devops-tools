package configsource

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
)

// side is a Source that records what it was asked to do, in order.
type side struct {
	files   map[string]string
	writes  []string
	failPut error
	gets    int32 // counted so an optimisation can be shown rather than claimed
}

func newSide(files map[string]string) *side {
	if files == nil {
		files = map[string]string{}
	}
	return &side{files: files}
}

func (s *side) Kind() string { return "fake" }
func (s *side) Root() string { return "root/" }
func (s *side) List(context.Context) ([]Entry, error) {
	out := make([]Entry, 0, len(s.files))
	for p := range s.files {
		out = append(out, Entry{Path: p, Version: "v"})
	}
	return out, nil
}
func (s *side) Get(_ context.Context, key string) (File, error) {
	atomic.AddInt32(&s.gets, 1)
	c, ok := s.files[key]
	if !ok {
		return File{}, ErrNotFound
	}
	return File{Entry: Entry{Path: key, Version: "17"}, Content: c}, nil
}
func (s *side) Put(_ context.Context, key, content, _ string, _ Editor) error {
	if s.failPut != nil {
		return s.failPut
	}
	s.writes = append(s.writes, "put:"+key)
	s.files[key] = content
	return nil
}
func (s *side) PutUnchecked(_ context.Context, key, content string) error {
	if s.failPut != nil {
		return s.failPut
	}
	s.writes = append(s.writes, "unchecked:"+key)
	s.files[key] = content
	return nil
}

const cfgPath = "abs/application.yml"

// The commit lands before Consul is touched. The other order would make a
// change live before anything recorded who made it — and if the commit then
// failed, the synchroniser would quietly undo it, leaving nothing at all.
func TestConsulIsWrittenOnlyAfterTheCommit(t *testing.T) {
	git := newSide(map[string]string{cfgPath: "old"})
	consul := newSide(map[string]string{cfgPath: "old"})
	p := NewPair(git, consul)

	if err := p.Put(context.Background(), cfgPath, "new", "v1", Editor{Username: "ada"}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if len(git.writes) != 1 || git.writes[0] != "put:"+cfgPath {
		t.Errorf("git writes = %v, want one ordinary put", git.writes)
	}
	// Unchecked, not a version-checked put: Consul is a copy, and the
	// synchroniser overwrites it unconditionally too.
	if len(consul.writes) != 1 || consul.writes[0] != "unchecked:"+cfgPath {
		t.Errorf("consul writes = %v, want one unchecked put", consul.writes)
	}
	if consul.files[cfgPath] != "new" {
		t.Errorf("consul holds %q, want the new content", consul.files[cfgPath])
	}
}

// A refused commit leaves Consul untouched. Otherwise the change is live with
// no record of it, and the next synchroniser pass silently reverts it.
func TestRefusedCommitLeavesConsulAlone(t *testing.T) {
	git := newSide(map[string]string{cfgPath: "old"})
	git.failPut = ErrConflict
	consul := newSide(map[string]string{cfgPath: "old"})
	p := NewPair(git, consul)

	if err := p.Put(context.Background(), cfgPath, "new", "stale", Editor{}); err != ErrConflict {
		t.Fatalf("Put err = %v, want ErrConflict", err)
	}
	if len(consul.writes) != 0 {
		t.Errorf("consul was written %v after a refused commit; want nothing", consul.writes)
	}
	if consul.files[cfgPath] != "old" {
		t.Error("consul changed although the commit was refused")
	}
}

// A Consul write that fails after a successful commit is a delay, not a
// failure: the synchroniser brings the change across on its own. It has to be
// distinguishable, or the page invites a second save of something already
// committed — which would then be refused as a conflict against itself.
func TestConsulFailingAfterTheCommitIsADelayNotAFailure(t *testing.T) {
	git := newSide(map[string]string{cfgPath: "old"})
	consul := newSide(map[string]string{cfgPath: "old"})
	consul.failPut = errors.New("connection refused")
	p := NewPair(git, consul)

	err := p.Put(context.Background(), cfgPath, "new", "v1", Editor{})
	if !errors.Is(err, ErrSecondaryLagging) {
		t.Fatalf("Put err = %v, want it to be recognisable as a lagging secondary", err)
	}
	if errors.Is(err, ErrConflict) {
		t.Error("a lagging secondary is being reported as a conflict")
	}
	if len(git.writes) != 1 {
		t.Error("the commit did not land, so this is not the case being described")
	}
}

// The emergency path writes Consul and only Consul, leaving the repository
// behind on purpose. Committing as well would defeat the point of it.
func TestConsulOnlyLeavesTheRepositoryBehind(t *testing.T) {
	git := newSide(map[string]string{cfgPath: "old"})
	consul := newSide(map[string]string{cfgPath: "old"})
	p := NewPair(git, consul)

	if err := p.PutSide(context.Background(), SideConsul, cfgPath, "hotfix", Editor{}); err != nil {
		t.Fatalf("PutSide: %v", err)
	}
	if len(git.writes) != 0 {
		t.Errorf("the repository was written %v; the emergency path must not commit", git.writes)
	}
	if consul.files[cfgPath] != "hotfix" {
		t.Error("consul does not hold the emergency change")
	}
}

func TestCompareReportsHowTheSidesStand(t *testing.T) {
	cases := []struct {
		name       string
		git, consl map[string]string
		want       string
	}{
		{"identical", map[string]string{cfgPath: "a"}, map[string]string{cfgPath: "a"}, DriftMatch},
		{"different", map[string]string{cfgPath: "a"}, map[string]string{cfgPath: "b"}, DriftDiffer},
		{"not applied yet", map[string]string{cfgPath: "a"}, nil, DriftMissingInConsul},
		{"never committed", nil, map[string]string{cfgPath: "b"}, DriftMissingInGit},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := NewPair(newSide(c.git), newSide(c.consl))
			d, err := p.Compare(context.Background(), cfgPath)
			if err != nil {
				t.Fatalf("Compare: %v", err)
			}
			if d.State != c.want {
				t.Errorf("state = %q, want %q", d.State, c.want)
			}
		})
	}
}

// The synchroniser here drops the file's final newline. Treating that as drift
// would put every file permanently in "differs", and an indicator that is
// always on is one nobody reads. For YAML it changes nothing that any service
// could observe.
func TestTrailingNewlineIsNotDrift(t *testing.T) {
	const body = "spring:\n  datasource:\n    url: jdbc:oracle:thin:@host"
	p := NewPair(
		newSide(map[string]string{cfgPath: body + "\n"}),
		newSide(map[string]string{cfgPath: body}),
	)
	d, err := p.Compare(context.Background(), cfgPath)
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	if d.State != DriftMatch {
		t.Errorf("state = %q, want %q — only the final newline differs", d.State, DriftMatch)
	}
}

// Indentation is meaning in YAML. Normalising it away would hide real
// differences behind the very signal meant to reveal them.
func TestIndentationIsStillDrift(t *testing.T) {
	p := NewPair(
		newSide(map[string]string{cfgPath: "spring:\n  url: a\n"}),
		newSide(map[string]string{cfgPath: "spring:\n    url: a\n"}),
	)
	d, err := p.Compare(context.Background(), cfgPath)
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	if d.State != DriftDiffer {
		t.Errorf("state = %q, want %q — the indentation differs", d.State, DriftDiffer)
	}
}

// Neither side having it is not drift, it is a missing file, and saying
// "differs" about something that does not exist sends people looking for a
// difference they will never find.
func TestCompareOnAMissingPathIsNotFound(t *testing.T) {
	p := NewPair(newSide(nil), newSide(nil))
	if _, err := p.Compare(context.Background(), cfgPath); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Compare err = %v, want ErrNotFound", err)
	}
}

// The audit note describes Consul's prior state without reproducing it. These
// files hold database and mail passwords; an audit trail that quotes them turns
// every reader of the log into someone who has seen every credential.
func TestAuditNoteDescribesWithoutQuoting(t *testing.T) {
	const secret = "spring:\n  mail:\n    password: hunter2\n"
	p := NewPair(newSide(nil), newSide(map[string]string{cfgPath: secret}))

	note := p.SecondaryState(context.Background(), cfgPath, "something else")
	if strings.Contains(note, "hunter2") || strings.Contains(note, "password") {
		t.Fatalf("the audit note reproduces the file: %q", note)
	}
	if !strings.Contains(note, "17") {
		t.Errorf("note = %q; it should identify the version it saw", note)
	}

	if got := p.SecondaryState(context.Background(), cfgPath, secret); got != "identical" {
		t.Errorf("note for an unchanged entry = %q, want %q", got, "identical")
	}
	if got := p.SecondaryState(context.Background(), "nowhere/x.yml", ""); got != "absent" {
		t.Errorf("note for a missing entry = %q, want %q", got, "absent")
	}
}

// Drift never claims which side is newer: Consul records no time at all, so an
// ordering would be invented — and acting on an invented one means overwriting
// the wrong side.
func TestDriftStatesClaimNoOrdering(t *testing.T) {
	for _, state := range []string{DriftMatch, DriftDiffer, DriftMissingInConsul, DriftMissingInGit} {
		if strings.Contains(state, "newer") || strings.Contains(state, "older") {
			t.Errorf("drift state %q claims an ordering that cannot be established", state)
		}
	}
}

// Opening a file asks two questions — what is in it, and whether the sides
// agree — and each side must be read exactly once to answer both. Asking
// separately read the repository twice: once for the content and once more
// inside the comparison, each a round trip to GitLab, one after the other.
func TestOpeningAFileReadsEachSideOnce(t *testing.T) {
	git := newSide(map[string]string{cfgPath: "a"})
	consul := newSide(map[string]string{cfgPath: "a"})
	p := NewPair(git, consul)

	f, d, err := p.GetWithDrift(context.Background(), SideGit, cfgPath)
	if err != nil {
		t.Fatalf("GetWithDrift: %v", err)
	}
	if f.Content != "a" {
		t.Errorf("content = %q, want the file from the requested side", f.Content)
	}
	if d.State != DriftMatch {
		t.Errorf("state = %q, want %q", d.State, DriftMatch)
	}
	if n := atomic.LoadInt32(&git.gets); n != 1 {
		t.Errorf("read the repository %d times, want 1", n)
	}
	if n := atomic.LoadInt32(&consul.gets); n != 1 {
		t.Errorf("read Consul %d times, want 1", n)
	}
}

// Looking at the Consul side returns the Consul file, and still says how the
// two stand — the badge means the same thing whichever side is on screen.
func TestOpeningTheConsulSideReturnsConsulContent(t *testing.T) {
	p := NewPair(
		newSide(map[string]string{cfgPath: "from git"}),
		newSide(map[string]string{cfgPath: "from consul"}),
	)
	f, d, err := p.GetWithDrift(context.Background(), SideConsul, cfgPath)
	if err != nil {
		t.Fatalf("GetWithDrift: %v", err)
	}
	if f.Content != "from consul" {
		t.Errorf("content = %q, want the Consul side", f.Content)
	}
	if d.State != DriftDiffer {
		t.Errorf("state = %q, want %q", d.State, DriftDiffer)
	}
}

// A file that exists only in the repository still opens: it is the normal state
// between a commit and the synchroniser's next pass, and refusing to show it
// would be refusing to show the change that was just made.
func TestOpeningAFileConsulDoesNotHaveYet(t *testing.T) {
	p := NewPair(newSide(map[string]string{cfgPath: "new"}), newSide(nil))
	f, d, err := p.GetWithDrift(context.Background(), SideGit, cfgPath)
	if err != nil {
		t.Fatalf("GetWithDrift: %v", err)
	}
	if f.Content != "new" {
		t.Errorf("content = %q, want the file", f.Content)
	}
	if d.State != DriftMissingInConsul {
		t.Errorf("state = %q, want %q", d.State, DriftMissingInConsul)
	}
}
