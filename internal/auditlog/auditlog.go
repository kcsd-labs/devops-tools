// Package auditlog reads the audit trail back.
//
// Nothing is stored for it: the entries are the lines the service already
// writes to stdout. This package only decides where to read them from — Loki
// when the deployment has one, the service's own pod logs when it does not —
// and reads backwards in windows, so neither source is handed a range it will
// refuse.
package auditlog

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"devops-tools/internal/audit"
	"devops-tools/internal/loki"
)

// Source names where a page came from, so the interface can say how far back
// the reader may trust it.
type Source string

const (
	SourceLoki Source = "loki"
	SourcePods Source = "pods"
	SourceNone Source = ""
)

// window is the span of a single query. Loki caps the length of one query
// (max_query_length, often 721h) and the cap is not reported anywhere, so we
// never approach it: a long period is walked in windows instead.
const window = 24 * time.Hour

// steps bounds how far one request walks back through empty windows. A quiet
// night should not look like the end of the trail, but nor should one request
// scan a year.
const steps = 7

// podHorizon is what the pod-log source honestly covers: whatever the node has
// not rotated away yet, which is hours rather than days.
const podHorizon = 12 * time.Hour

// Scope is what the caller may see. It is built from their roles and applied
// inside the query rather than to the results: filtering a page afterwards
// returns short pages and makes "load more" lie about where it stopped.
type Scope struct {
	Namespaces []string // the namespaces this caller may read
	All        bool     // every namespace — a role granted "*"
	Global     bool     // entries belonging to no namespace: sign-ins, grants
}

// Empty reports a caller who may see nothing at all.
func (s Scope) Empty() bool { return !s.All && !s.Global && len(s.Namespaces) == 0 }

// Filter is what the reader asked for on top of their scope.
type Filter struct {
	Operation string
	User      string
	Text      string
	To        time.Time // read backwards from here; zero means now
	// From is the period the reader chose. It only ever narrows: the horizon
	// still applies, so a picker offering more than the deployment keeps cannot
	// turn into a query for it.
	From  time.Time
	Limit int
}

// Page is one screenful, newest first.
type Page struct {
	Entries []audit.Entry
	// Next is where the following page starts. Zero once the horizon is
	// reached, which is what tells the interface to stop offering more.
	Next time.Time
}

// Self is how the service finds its own logs. Without it there is no trail to
// read: in Loki the stream is selected by these labels, and the pod source
// lists pods by them.
type Self struct {
	Namespace string // the namespace the service runs in
	App       string // its app label, as the log collector records it
	Selector  string // label selector matching its pods
}

// Reader answers queries from whichever source the deployment has.
type Reader struct {
	self    Self
	loki    *loki.Client
	pods    *podSource
	horizon time.Duration
}

// New picks a source. Loki wins when configured: it holds history the node has
// long since rotated away.
func New(self Self, lokiClient *loki.Client, pods PodLister, horizon time.Duration) *Reader {
	r := &Reader{self: self, horizon: horizon}
	if self.Namespace == "" {
		return r // cannot find its own logs; Source reports none
	}
	if lokiClient != nil {
		r.loki = lokiClient
		if r.horizon == 0 {
			r.horizon = 30 * 24 * time.Hour
		}
		return r
	}
	if pods != nil {
		r.pods = &podSource{lister: pods, self: self}
		r.horizon = podHorizon
	}
	return r
}

// Source says where entries come from, for the interface to show.
func (r *Reader) Source() Source {
	switch {
	case r.loki != nil:
		return SourceLoki
	case r.pods != nil:
		return SourcePods
	default:
		return SourceNone
	}
}

// Horizon is how far back this reader will go.
func (r *Reader) Horizon() time.Duration { return r.horizon }

// Read returns one page, newest first.
func (r *Reader) Read(ctx context.Context, sc Scope, f Filter) (Page, error) {
	if r.Source() == SourceNone {
		return Page{}, fmt.Errorf("no audit source: neither Loki nor this service's own pod logs are reachable")
	}
	if sc.Empty() {
		return Page{}, nil
	}
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 200
	}
	now := time.Now()
	to := f.To
	if to.IsZero() || to.After(now) {
		to = now
	}
	floor := now.Add(-r.horizon)
	if !f.From.IsZero() && f.From.After(floor) {
		floor = f.From
	}

	var out []audit.Entry
	for step := 0; step < steps && to.After(floor) && len(out) < f.Limit; step++ {
		from := to.Add(-window)
		if from.Before(floor) {
			from = floor
		}
		batch, err := r.read(ctx, sc, f, from, to, f.Limit-len(out))
		if err != nil {
			return Page{}, err
		}
		out = append(out, batch...)
		to = from
	}

	next := to
	if !next.After(floor) {
		next = time.Time{} // the horizon, not a cursor
	}
	return Page{Entries: out, Next: next}, nil
}

func (r *Reader) read(ctx context.Context, sc Scope, f Filter, from, to time.Time, limit int) ([]audit.Entry, error) {
	if r.pods != nil {
		return r.pods.read(ctx, sc, f, from, to, limit)
	}
	lines, err := r.loki.QueryRange(ctx, r.logql(sc, f), from, to, limit, "backward")
	if err != nil {
		return nil, err
	}
	out := make([]audit.Entry, 0, len(lines))
	for _, l := range lines {
		if e, ok := audit.Parse([]byte(l.Line)); ok && keep(e, sc, f) {
			out = append(out, e)
		}
	}
	return out, nil
}

// logql builds the query. The scope goes in here rather than into a pass over
// the results, and the field names are extracted under new labels because the
// stream already carries a `namespace` label of its own — the service's, not
// the one the action touched.
func (r *Reader) logql(sc Scope, f Filter) string {
	q := fmt.Sprintf("{namespace=%q,app=%q}", r.self.Namespace, r.self.App)

	// A line filter before parsing is the cheap one, so free text goes first.
	if f.Text != "" {
		q += fmt.Sprintf(" |~ %q", "(?i)"+regexp.QuoteMeta(f.Text))
	}
	q += ` | json msg="msg", ns="namespace", op="operation", usr="user" | msg="action"`

	switch {
	case sc.All && sc.Global:
		// everything
	case sc.All:
		q += ` | ns!=""`
	case len(sc.Namespaces) > 0:
		alt := make([]string, 0, len(sc.Namespaces)+1)
		for _, ns := range sc.Namespaces {
			alt = append(alt, regexp.QuoteMeta(ns))
		}
		if sc.Global {
			alt = append(alt, "") // an empty branch matches the entries with no namespace
		}
		q += fmt.Sprintf(" | ns=~%q", strings.Join(alt, "|"))
	case sc.Global:
		q += ` | ns=""`
	}

	if f.Operation != "" {
		q += fmt.Sprintf(" | op=%q", f.Operation)
	}
	if f.User != "" {
		q += fmt.Sprintf(" | usr=~%q", "(?i).*"+regexp.QuoteMeta(f.User)+".*")
	}
	return q
}

// keep re-applies the scope to a parsed entry. The query already narrows the
// stream; this is the second gate, so that a label the collector renamed or a
// query the parser read differently cannot widen what somebody sees.
func keep(e audit.Entry, sc Scope, f Filter) bool {
	if e.Namespace == "" || e.Namespace == "-" {
		if !sc.Global {
			return false
		}
	} else if !sc.All {
		found := false
		for _, ns := range sc.Namespaces {
			if ns == e.Namespace {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	if f.Operation != "" && e.Operation != f.Operation {
		return false
	}
	if f.User != "" && !strings.Contains(strings.ToLower(e.User), strings.ToLower(f.User)) {
		return false
	}
	if f.Text != "" {
		hay := strings.ToLower(e.User + " " + e.Namespace + " " + e.Operation + " " + e.Target + " " + e.Error)
		if !strings.Contains(hay, strings.ToLower(f.Text)) {
			return false
		}
	}
	return true
}

func sortNewestFirst(es []audit.Entry) {
	sort.SliceStable(es, func(i, j int) bool { return es[i].Time.After(es[j].Time) })
}
