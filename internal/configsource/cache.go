package configsource

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// listTTL is how long a listing is considered current.
//
// Short enough that a file added outside the portal shows up without anyone
// wondering why it has not. Reading a file and saving it never go through
// here — content and version must always be current, or a save would be checked
// against a version that has moved on.
const listTTL = 30 * time.Second

// staleLimit is how long a listing may still be served after it has gone out of
// date, while a fresh one is fetched behind it.
//
// Bounded, because "the last thing we knew" stops being an answer at some
// point. If the far side has been unreachable this long, waiting for the real
// error is better than serving something from before lunch as though it were
// the truth.
const staleLimit = 10 * time.Minute

// Cache makes listings cheap, and keeps them off the critical path.
//
// Listing is the expensive call: against Git it is a recursive tree walk over
// the API, paged, and the portal asks for it whenever the Configurations page
// opens. Two things follow from that. The first visit after a restart should
// not be the one that pays for it — see Warm. And once there is a listing,
// nobody should wait for the next one: an expired listing is served as it
// stands while a fresh one is fetched behind it.
func Cache(s Source) *Cached { return &Cached{Source: s} }

type Cached struct {
	Source
	mu         sync.Mutex
	entries    []Entry
	at         time.Time
	refreshing bool
}

func (c *Cached) List(ctx context.Context) ([]Entry, error) {
	c.mu.Lock()
	entries, at := c.entries, c.at
	if entries != nil && time.Since(at) < staleLimit {
		if time.Since(at) >= listTTL && !c.refreshing {
			// Out of date but usable. Hand it over and bring it up to date in
			// the background, so the cost lands between visits rather than in
			// front of one.
			c.refreshing = true
			go c.refresh()
		}
		c.mu.Unlock()
		return entries, nil
	}
	// Nothing usable yet. The lock is held across the call on purpose: several
	// tabs opening at once then make one request between them, not one each.
	defer c.mu.Unlock()
	fresh, err := c.Source.List(ctx)
	if err != nil {
		// A failure is not cached. Otherwise one timeout would keep the page
		// empty for the rest of the interval, long after the cause had passed.
		return nil, err
	}
	c.entries, c.at = fresh, time.Now()
	return fresh, nil
}

// Warm fetches the listing once, in the background, so the first person to open
// the page does not pay for it. Failure is logged and forgotten: an
// installation whose GitLab is briefly unreachable at startup should still
// start, and the next real request will report the problem properly.
func (c *Cached) Warm() {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		if _, err := c.List(ctx); err != nil {
			slog.Warn("could not read the configuration listing at startup; "+
				"the first visit to Configurations will wait for it", "error", err)
			return
		}
		slog.Info("configuration listing ready")
	}()
}

func (c *Cached) refresh() {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	fresh, err := c.Source.List(ctx)

	c.mu.Lock()
	defer c.mu.Unlock()
	c.refreshing = false
	if err != nil {
		// Keep what we have and try again on the next request. Discarding it
		// would turn a momentary failure into an empty page.
		slog.Warn("could not refresh the configuration listing", "error", err)
		return
	}
	c.entries, c.at = fresh, time.Now()
}

func (c *Cached) Put(ctx context.Context, path, content, version string, by Editor) error {
	err := c.Source.Put(ctx, path, content, version, by)
	if err == nil {
		// A file created through the portal has to appear in the tree at once.
		// Waiting out the interval would look like the save had not worked.
		c.mu.Lock()
		c.entries, c.at = nil, time.Time{}
		c.mu.Unlock()
	}
	return err
}
