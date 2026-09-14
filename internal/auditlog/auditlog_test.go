package auditlog

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"devops-tools/internal/audit"
)

func TestOnlyActionLinesAreRead(t *testing.T) {
	cases := []struct {
		name string
		line string
		want bool
	}{
		{"an action", `{"time":"2026-09-13T08:41:02Z","level":"INFO","msg":"action","user":"a.sagyn","namespace":"payments-prod","operation":"secret-read","target":"db-credentials","allowed":true,"success":true,"error":""}`, true},
		{"another log from the same stream", `{"time":"2026-09-13T08:41:02Z","level":"INFO","msg":"server starting","addr":":8080"}`, false},
		{"a line that is not JSON at all", `panic: runtime error`, false},
		{"an empty line", ``, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e, ok := audit.Parse([]byte(c.line))
			if ok != c.want {
				t.Fatalf("parsed=%v, want %v", ok, c.want)
			}
			if ok && e.User != "a.sagyn" {
				t.Fatalf("user=%q", e.User)
			}
		})
	}
}

func TestScopeGoesIntoTheQuery(t *testing.T) {
	r := &Reader{self: Self{Namespace: "devops-tools", App: "devops-tools"}}

	t.Run("named namespaces, no global grant", func(t *testing.T) {
		q := r.logql(Scope{Namespaces: []string{"payments-dev", "payments-stage"}}, Filter{})
		if !strings.Contains(q, `ns=~"payments-dev|payments-stage"`) {
			t.Fatalf("query does not narrow to the granted namespaces: %s", q)
		}
	})

	t.Run("a global grant adds the entries with no namespace", func(t *testing.T) {
		q := r.logql(Scope{Namespaces: []string{"payments-dev"}, Global: true}, Filter{})
		// The trailing empty branch is what matches an entry whose namespace is
		// empty — a sign-in, or a role being granted.
		if !strings.Contains(q, `ns=~"payments-dev|"`) {
			t.Fatalf("global entries are not included: %s", q)
		}
	})

	t.Run("a wildcard grant without global still hides sign-ins", func(t *testing.T) {
		q := r.logql(Scope{All: true}, Filter{})
		if !strings.Contains(q, `ns!=""`) {
			t.Fatalf("entries with no namespace are not excluded: %s", q)
		}
	})

	t.Run("free text is a line filter, before parsing", func(t *testing.T) {
		q := r.logql(Scope{All: true, Global: true}, Filter{Text: "db-credentials"})
		line := strings.Index(q, "|~")
		parse := strings.Index(q, "| json")
		if line < 0 || parse < 0 || line > parse {
			t.Fatalf("the cheap filter should come first: %s", q)
		}
	})

	t.Run("regex metacharacters in a namespace cannot widen the query", func(t *testing.T) {
		q := r.logql(Scope{Namespaces: []string{"pay.*"}}, Filter{})
		if strings.Contains(q, `ns=~"pay.*"`) {
			t.Fatalf("a namespace was interpolated unescaped: %s", q)
		}
	})
}

func TestTheResultIsCheckedAgain(t *testing.T) {
	// The query already narrows the stream. This is the second gate: a label the
	// collector renamed, or a parser that read a field differently, must not be
	// able to widen what somebody sees.
	inside := audit.Entry{Namespace: "payments-dev", Operation: "logs", User: "i.petrov"}
	outside := audit.Entry{Namespace: "billing-prod", Operation: "logs", User: "i.petrov"}
	global := audit.Entry{Namespace: "", Operation: "user-manage", User: "a.sagyn"}

	sc := Scope{Namespaces: []string{"payments-dev"}}
	if !keep(inside, sc, Filter{}) {
		t.Error("an entry from a granted namespace was dropped")
	}
	if keep(outside, sc, Filter{}) {
		t.Error("an entry from a namespace the caller has no grant in was kept")
	}
	if keep(global, sc, Filter{}) {
		t.Error("an entry with no namespace was kept without a global grant")
	}
	if !keep(global, Scope{Global: true}, Filter{}) {
		t.Error("a global grant did not admit an entry with no namespace")
	}
	if !keep(outside, Scope{All: true}, Filter{}) {
		t.Error("a wildcard grant did not admit a namespaced entry")
	}
}

// fakePods serves canned log streams, so the reading and merging can be tested
// without a cluster.
type fakePods struct {
	pods map[string]string
}

func (f fakePods) OwnPods(_ context.Context, _, _ string) ([]string, error) {
	names := make([]string, 0, len(f.pods))
	for n := range f.pods {
		names = append(names, n)
	}
	return names, nil
}

func (f fakePods) OwnPodLogs(_ context.Context, _, pod string, _ time.Time, previous bool) (io.ReadCloser, error) {
	if previous {
		return io.NopCloser(strings.NewReader("")), nil
	}
	return io.NopCloser(strings.NewReader(f.pods[pod])), nil
}

func TestPodLogsAreMergedNewestFirst(t *testing.T) {
	line := func(ts, ns, op string) string {
		return `{"time":"` + ts + `","msg":"action","user":"i.petrov","namespace":"` + ns +
			`","operation":"` + op + `","allowed":true,"success":true}` + "\n"
	}
	src := &podSource{
		self: Self{Namespace: "devops-tools"},
		lister: fakePods{pods: map[string]string{
			"devops-tools-a": line("2026-09-13T08:00:00Z", "payments-dev", "logs") +
				"not json at all\n" +
				line("2026-09-13T10:00:00Z", "payments-dev", "pod-restart"),
			"devops-tools-b": line("2026-09-13T09:00:00Z", "payments-dev", "describe") +
				line("2026-09-13T09:30:00Z", "billing-prod", "logs"),
		}},
	}

	from := time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC)
	got, err := src.read(context.Background(), Scope{Namespaces: []string{"payments-dev"}}, Filter{}, from, to, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d entries, want 3 (the billing-prod one is out of scope)", len(got))
	}
	for i := 1; i < len(got); i++ {
		if got[i].Time.After(got[i-1].Time) {
			t.Fatalf("entries are not newest first: %v", got)
		}
	}
	if got[0].Operation != "pod-restart" {
		t.Fatalf("newest entry is %q", got[0].Operation)
	}
}

func TestAPageStopsAtTheHorizon(t *testing.T) {
	r := New(Self{Namespace: "devops-tools", App: "devops-tools"}, nil,
		fakePods{pods: map[string]string{}}, 0)
	if r.Source() != SourcePods {
		t.Fatalf("source=%q, want the pod fallback", r.Source())
	}
	page, err := r.Read(context.Background(), Scope{All: true}, Filter{To: time.Now().Add(-48 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if !page.Next.IsZero() {
		t.Fatal("a cursor was offered past the horizon, so the interface would keep asking for nothing")
	}
}

func TestTheChosenPeriodNarrowsButCannotWiden(t *testing.T) {
	// A picker offering more than the deployment keeps — a stale page, or a
	// crafted request — must not turn into a query for it.
	r := New(Self{Namespace: "devops-tools", App: "devops-tools"}, nil,
		fakePods{pods: map[string]string{}}, 0) // pod source: a 12h horizon
	page, err := r.Read(context.Background(), Scope{All: true},
		Filter{From: time.Now().Add(-365 * 24 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if !page.Next.IsZero() && time.Since(page.Next) > 13*time.Hour {
		t.Fatalf("the cursor reached past the horizon: %v", page.Next)
	}
}

func TestAPageIsNewestFirstAcrossWindows(t *testing.T) {
	// One page is assembled from several windows, walking backwards, and each
	// window arrives oldest first. Without ordering the whole page the result
	// is neither: blocks descending, rows inside them ascending.
	line := func(t time.Time) string {
		return `{"time":"` + t.UTC().Format(time.RFC3339) +
			`","msg":"action","user":"i.petrov","namespace":"payments-dev",` +
			`"operation":"logs","allowed":true,"success":true}` + "\n"
	}
	now := time.Now().UTC()
	var log string
	for _, h := range []int{2, 26, 50} { // one entry in each of three windows
		log += line(now.Add(-time.Duration(h) * time.Hour))
	}
	r := &Reader{
		self:    Self{Namespace: "devops-tools"},
		pods:    &podSource{self: Self{Namespace: "devops-tools"}, lister: fakePods{pods: map[string]string{"a": log}}},
		horizon: 72 * time.Hour,
	}

	page, err := r.Read(context.Background(), Scope{All: true}, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Entries) != 3 {
		t.Fatalf("got %d entries, want 3 — one per window", len(page.Entries))
	}
	for i := 1; i < len(page.Entries); i++ {
		if page.Entries[i].Time.After(page.Entries[i-1].Time) {
			t.Fatalf("page is not newest first: %v", page.Entries)
		}
	}
}

func TestWithoutASourceTheReaderSaysSo(t *testing.T) {
	r := New(Self{}, nil, nil, 0)
	if r.Source() != SourceNone {
		t.Fatalf("source=%q", r.Source())
	}
	if _, err := r.Read(context.Background(), Scope{All: true}, Filter{}); err == nil {
		t.Fatal("reading without a source should explain itself, not return an empty page")
	}
}
