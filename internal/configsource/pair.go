package configsource

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Side names, as they appear in the API and the interface.
const (
	SideGit    = "git"
	SideConsul = "consul"
)

// ErrSecondaryLagging means the commit landed but Consul was not updated.
//
// Not a failure of the save: the synchroniser copies the repository across on
// its own schedule, so the change arrives regardless — just later than it would
// have. The person is told, because until then the running services still have
// the old values, and that is the difference between "saved" and "in effect".
var ErrSecondaryLagging = errors.New("committed to Git, but Consul was not updated directly")

// Dual is a source with two sides. The API asks for it with a type assertion,
// so a deployment with one source is untouched by any of this.
type Dual interface {
	Source
	// Sides lists the sides, primary first.
	Sides() []string
	Side(name string) (Source, bool)
	// PutSide writes one side and only that side. This is the emergency path:
	// Consul alone, when a service has to be fixed now and the repository can
	// wait. What it leaves behind is a difference that the page then shows.
	PutSide(ctx context.Context, side, path, content string, by Editor) error
	// Compare reports how the two sides stand on one path.
	Compare(ctx context.Context, path string) (Drift, error)
	// GetWithDrift returns the file from one side and how the two stand,
	// reading each side once. Opening a file needs both answers, and asking
	// for them separately reads the same file twice.
	GetWithDrift(ctx context.Context, side, path string) (File, Drift, error)
	// SecondaryState describes Consul before it is overwritten, for the audit
	// record. It returns a description, never content — see the implementation.
	SecondaryState(ctx context.Context, path, expected string) string
}

// Drift is how the two sides stand on one path.
type Drift struct {
	// State is one of "match", "differ", "missing-in-consul", "missing-in-git".
	State string `json:"state"`
}

const (
	DriftMatch           = "match"
	DriftDiffer          = "differ"
	DriftMissingInConsul = "missing-in-consul"
	DriftMissingInGit    = "missing-in-git"
)

// Pair is a Git repository with the Consul store a synchroniser copies it into.
//
// The repository is the source of truth: listing and reading come from it, and
// a save is a commit. Consul is then brought up to date immediately rather than
// at the synchroniser's next pass, because until it is, the change is saved but
// not in effect.
type Pair struct {
	git    Source
	consul Secondary
}

// Secondary is the side that is written without a version check. An interface
// rather than *Consul so the ordering — the part worth being sure of — can be
// tested without a live agent.
type Secondary interface {
	Source
	PutUnchecked(ctx context.Context, path, content string) error
}

func NewPair(git Source, consul Secondary) *Pair { return &Pair{git: git, consul: consul} }

func (p *Pair) Kind() string { return "git+consul" }
func (p *Pair) Root() string { return p.git.Root() }

func (p *Pair) Sides() []string { return []string{SideGit, SideConsul} }

func (p *Pair) Side(name string) (Source, bool) {
	switch name {
	case SideGit:
		return p.git, true
	case SideConsul:
		return p.consul, true
	}
	return nil, false
}

// List comes from the repository, which decides what is supposed to exist.
// Anything in Consul and not in the repository is shown by switching sides —
// it is a finding, not part of the normal list.
func (p *Pair) List(ctx context.Context) ([]Entry, error) { return p.git.List(ctx) }

func (p *Pair) Get(ctx context.Context, path string) (File, error) { return p.git.Get(ctx, path) }

// Put commits to the repository, then writes Consul.
//
// In that order, deliberately. Consul first would mean a change that is live
// before there is any record of who made it, and if the commit then failed the
// synchroniser would quietly undo it later, leaving nothing behind at all. This
// way a failure after the commit is only a delay, and the message says so.
func (p *Pair) Put(ctx context.Context, path, content, version string, by Editor) error {
	if err := p.git.Put(ctx, path, content, version, by); err != nil {
		return err
	}
	if err := p.consul.PutUnchecked(ctx, path, content); err != nil {
		return fmt.Errorf("%w: %v", ErrSecondaryLagging, err)
	}
	return nil
}

func (p *Pair) PutSide(ctx context.Context, side, path, content string, by Editor) error {
	switch side {
	case SideConsul:
		// Unconditional, as the synchroniser is. Comparing versions here would
		// be protecting a copy, and the copy is what is being deliberately
		// diverged from.
		return p.consul.PutUnchecked(ctx, path, content)
	case SideGit:
		return errors.New("writing only the repository is the ordinary save; use it instead")
	}
	return fmt.Errorf("unknown side %q", side)
}

// Compare reads both sides and says how they stand.
//
// Which one is newer is deliberately not claimed: Consul records no time at all,
// so any ordering would be invented. What matters is answerable — whether the
// running services have what the repository says they should.
func (p *Pair) Compare(ctx context.Context, path string) (Drift, error) {
	g, c, gerr, cerr := p.bothSides(ctx, path)
	return drift(g, c, gerr, cerr)
}

// GetWithDrift reads both sides once, concurrently, and answers both questions
// the page asks when a file is opened: what is in it, and whether the two sides
// agree. Asking separately meant reading the same file twice and waiting for
// each read in turn — four round trips where two, side by side, will do.
func (p *Pair) GetWithDrift(ctx context.Context, side, path string) (File, Drift, error) {
	g, c, gerr, cerr := p.bothSides(ctx, path)

	wanted, err := g, gerr
	if side == SideConsul {
		wanted, err = c, cerr
	}
	if err != nil {
		return File{}, Drift{}, err
	}
	d, derr := drift(g, c, gerr, cerr)
	if derr != nil {
		// The requested side was read; only the comparison could not be made.
		// Showing the file without a verdict beats showing nothing.
		return wanted, Drift{}, nil
	}
	return wanted, d, nil
}

// bothSides reads Git and Consul at the same time. One is a call over the
// network to GitLab and the other to Consul; there is no reason for either to
// wait for the other.
func (p *Pair) bothSides(ctx context.Context, path string) (g, c File, gerr, cerr error) {
	done := make(chan struct{})
	go func() {
		c, cerr = p.consul.Get(ctx, path)
		close(done)
	}()
	g, gerr = p.git.Get(ctx, path)
	<-done
	return g, c, gerr, cerr
}

func drift(g, c File, gerr, cerr error) (Drift, error) {
	if gerr != nil && !errors.Is(gerr, ErrNotFound) {
		return Drift{}, gerr
	}
	if cerr != nil && !errors.Is(cerr, ErrNotFound) {
		return Drift{}, cerr
	}
	switch {
	case errors.Is(gerr, ErrNotFound) && errors.Is(cerr, ErrNotFound):
		return Drift{}, ErrNotFound
	case errors.Is(cerr, ErrNotFound):
		return Drift{State: DriftMissingInConsul}, nil
	case errors.Is(gerr, ErrNotFound):
		return Drift{State: DriftMissingInGit}, nil
	case sameConfig(g.Content, c.Content):
		return Drift{State: DriftMatch}, nil
	default:
		return Drift{State: DriftDiffer}, nil
	}
}

// sameConfig compares two versions of a configuration file, ignoring how it
// ends.
//
// A synchroniser that drops the final newline — as the one here does — would
// otherwise put every file permanently in "differs", and an indicator that is
// always on is one nobody reads. Nothing else is normalised: indentation is
// meaning in YAML, and trimming it would hide real differences behind the very
// signal meant to reveal them.
func sameConfig(a, b string) bool {
	return strings.TrimRight(a, "\r\n") == strings.TrimRight(b, "\r\n")
}

// SecondaryState reports what is in Consul before it is overwritten, for the
// audit record.
//
// The content itself is never returned and never logged. These files carry
// database and mail passwords; an audit trail that quotes them turns every log
// reader into someone who has seen every credential in the estate. Whether it
// differed, and at which index, is what an incident review actually asks.
func (p *Pair) SecondaryState(ctx context.Context, path, expected string) string {
	f, err := p.consul.Get(ctx, path)
	switch {
	case errors.Is(err, ErrNotFound):
		return "absent"
	case err != nil:
		return "unknown"
	case sameConfig(f.Content, expected):
		return "identical"
	default:
		return "differed at index " + f.Version
	}
}
