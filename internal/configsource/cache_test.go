package configsource

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// counting is a Source that records how often it was asked.
type counting struct {
	mu    sync.Mutex
	lists int
	puts  int
	fail  error
	seen  []Entry
}

func (c *counting) Kind() string { return "test" }
func (c *counting) Root() string { return "root/" }
func (c *counting) List(context.Context) ([]Entry, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lists++
	if c.fail != nil {
		return nil, c.fail
	}
	return c.seen, nil
}

func (c *counting) calls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lists
}

func (c *counting) setFail(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.fail = err
}
func (c *counting) Get(context.Context, string) (File, error) { return File{}, ErrNotFound }
func (c *counting) Put(context.Context, string, string, string, Editor) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.puts++
	return c.fail
}

// The listing is what every visit to the page pays for, and against Git it is a
// paged walk of the whole repository. Opening the page twice must not walk it
// twice.
func TestListingIsReused(t *testing.T) {
	inner := &counting{seen: []Entry{{Path: "abs/application.yml"}}}
	c := Cache(inner)

	for i := 0; i < 5; i++ {
		if _, err := c.List(context.Background()); err != nil {
			t.Fatalf("List: %v", err)
		}
	}
	if inner.calls() != 1 {
		t.Errorf("asked the source %d times, want 1", inner.calls())
	}
}

// A file saved through the portal has to appear in the tree at once. Waiting
// out the interval would read as the save not having worked, and the obvious
// response — save again — is the one that causes real trouble.
func TestSavingRefreshesTheListing(t *testing.T) {
	inner := &counting{seen: []Entry{{Path: "abs/application.yml"}}}
	c := Cache(inner)

	if _, err := c.List(context.Background()); err != nil {
		t.Fatalf("List: %v", err)
	}
	if err := c.Put(context.Background(), "abs/new.yml", "k: v", "", Editor{}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if _, err := c.List(context.Background()); err != nil {
		t.Fatalf("List: %v", err)
	}
	if inner.calls() != 2 {
		t.Errorf("the listing was reused after a save (%d calls, want 2)", inner.calls())
	}
}

// A failed save must not throw the listing away: nothing changed, and the next
// visit would pay for a walk it did not need.
func TestFailedSaveKeepsTheListing(t *testing.T) {
	inner := &counting{seen: []Entry{{Path: "abs/application.yml"}}}
	c := Cache(inner)
	if _, err := c.List(context.Background()); err != nil {
		t.Fatalf("List: %v", err)
	}

	inner.setFail(ErrConflict)
	if err := c.Put(context.Background(), "abs/application.yml", "x", "old", Editor{}); err != ErrConflict {
		t.Fatalf("Put err = %v, want ErrConflict", err)
	}
	inner.setFail(nil)

	if _, err := c.List(context.Background()); err != nil {
		t.Fatalf("List: %v", err)
	}
	if inner.calls() != 1 {
		t.Errorf("a refused save discarded the listing (%d calls, want 1)", inner.calls())
	}
}

// A failure is never cached. Otherwise one timeout leaves the page empty for
// the rest of the interval, long after the cause has passed.
func TestFailuresAreNotCached(t *testing.T) {
	boom := errors.New("gitlab unreachable")
	inner := &counting{fail: boom}
	c := Cache(inner)

	if _, err := c.List(context.Background()); !errors.Is(err, boom) {
		t.Fatalf("List err = %v, want the source's error", err)
	}
	inner.setFail(nil)
	inner.seen = []Entry{{Path: "abs/application.yml"}}

	entries, err := c.List(context.Background())
	if err != nil {
		t.Fatalf("List after recovery: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("got %d entries after recovery, want 1 — the failure was cached", len(entries))
	}
}

// Content and version are never cached: a version read from a stale cache
// would be checked against an entry that has moved on, and the save it guards
// would be refused for no reason anyone could see.
func TestContentIsNotCached(t *testing.T) {
	inner := &counting{}
	c := Cache(inner)
	for i := 0; i < 3; i++ {
		if _, err := c.Get(context.Background(), "abs/application.yml"); err != ErrNotFound {
			t.Fatalf("Get err = %v, want it to reach the source every time", err)
		}
	}
}

// An expired listing is handed over as it stands while a fresh one is fetched
// behind it. Nobody should wait for a tree walk they did not ask for — and the
// list they get is at most half a minute behind, which for a set of files that
// changes weekly is no difference at all.
func TestAnExpiredListingIsServedWhileItIsRefreshed(t *testing.T) {
	inner := &counting{seen: []Entry{{Path: "abs/application.yml"}}}
	c := Cache(inner)

	if _, err := c.List(context.Background()); err != nil {
		t.Fatalf("List: %v", err)
	}
	expire(c, listTTL+time.Second)

	start := time.Now()
	entries, err := c.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if time.Since(start) > 50*time.Millisecond {
		t.Error("the caller waited for the refresh instead of being handed what was already known")
	}
	if len(entries) != 1 {
		t.Errorf("got %d entries, want the ones already known", len(entries))
	}

	waitFor(t, func() bool { return inner.calls() == 2 }, "the background refresh never ran")
}

// Only one refresh at a time. Ten tabs opening at once should not produce ten
// tree walks of the same repository.
func TestOnlyOneRefreshRunsAtATime(t *testing.T) {
	inner := &counting{seen: []Entry{{Path: "abs/application.yml"}}}
	c := Cache(inner)
	if _, err := c.List(context.Background()); err != nil {
		t.Fatalf("List: %v", err)
	}
	expire(c, listTTL+time.Second)

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = c.List(context.Background())
		}()
	}
	wg.Wait()
	waitFor(t, func() bool { return inner.calls() == 2 }, "the refresh did not finish")

	if n := inner.calls(); n != 2 {
		t.Errorf("the source was asked %d times, want 2 — the first read and one refresh", n)
	}
}

// "The last thing we knew" stops being an answer eventually. If the far side
// has been unreachable this long, the real error is more use than something
// from before lunch presented as the truth.
func TestAVeryStaleListingIsNotServed(t *testing.T) {
	boom := errors.New("gitlab unreachable")
	inner := &counting{seen: []Entry{{Path: "abs/application.yml"}}}
	c := Cache(inner)
	if _, err := c.List(context.Background()); err != nil {
		t.Fatalf("List: %v", err)
	}

	expire(c, staleLimit+time.Second)
	inner.setFail(boom)
	if _, err := c.List(context.Background()); !errors.Is(err, boom) {
		t.Fatalf("List err = %v, want the source's error rather than an ancient listing", err)
	}
}

// A refresh that fails keeps what is already known: a momentary failure should
// not turn into an empty page.
func TestAFailedRefreshKeepsTheOldListing(t *testing.T) {
	inner := &counting{seen: []Entry{{Path: "abs/application.yml"}}}
	c := Cache(inner)
	if _, err := c.List(context.Background()); err != nil {
		t.Fatalf("List: %v", err)
	}
	expire(c, listTTL+time.Second)
	inner.setFail(errors.New("gitlab unreachable"))

	entries, err := c.List(context.Background())
	if err != nil || len(entries) != 1 {
		t.Fatalf("List = %v, %v; want what was already known", entries, err)
	}
	waitFor(t, func() bool { return inner.calls() == 2 }, "the refresh never ran")

	entries, err = c.List(context.Background())
	if err != nil || len(entries) != 1 {
		t.Errorf("List = %v, %v; the failed refresh discarded the listing", entries, err)
	}
}

// expire backdates the cache so a test does not have to wait out the interval.
func expire(c *Cached, by time.Duration) {
	c.mu.Lock()
	c.at = c.at.Add(-by)
	c.mu.Unlock()
}

func waitFor(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal(msg)
}
