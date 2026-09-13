package auditlog

import (
	"bufio"
	"context"
	"io"
	"time"

	"devops-tools/internal/audit"
)

// PodLister is the part of Kubernetes this package needs. It is an interface so
// the reading and filtering can be tested without a cluster.
type PodLister interface {
	// Pods returns the names of the service's own pods.
	OwnPods(ctx context.Context, namespace, selector string) ([]string, error)
	// Logs returns one pod's stdout from a point in time. With previous set it
	// returns the container that ran before the last restart, which is where
	// the entries from before a crash are.
	OwnPodLogs(ctx context.Context, namespace, pod string, since time.Time, previous bool) (io.ReadCloser, error)
}

// podSource reads the trail out of the service's own pod logs.
//
// This is the fallback for a deployment with no Loki. It costs nothing — the
// service account already reads pod logs, that being the product's main
// function — but it only reaches as far back as the node has kept, and a pod
// that is deleted takes its share of the trail with it.
type podSource struct {
	lister PodLister
	self   Self
}

// maxScanned bounds one pass, so a chatty deployment cannot turn a page of the
// audit screen into an unbounded read.
const maxScanned = 200000

func (p *podSource) read(ctx context.Context, sc Scope, f Filter, from, to time.Time, limit int) ([]audit.Entry, error) {
	pods, err := p.lister.OwnPods(ctx, p.self.Namespace, p.self.Selector)
	if err != nil {
		return nil, err
	}
	var out []audit.Entry
	for _, pod := range pods {
		for _, previous := range []bool{false, true} {
			es, err := p.scan(ctx, pod, previous, sc, f, from, to)
			if err != nil {
				// A pod that never restarted has no previous container, and a
				// pod that went away between listing and reading is normal.
				// Neither is a reason to fail the page.
				continue
			}
			out = append(out, es...)
		}
	}
	sortNewestFirst(out)
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (p *podSource) scan(ctx context.Context, pod string, previous bool, sc Scope, f Filter, from, to time.Time) ([]audit.Entry, error) {
	rc, err := p.lister.OwnPodLogs(ctx, p.self.Namespace, pod, from, previous)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rc.Close() }()

	var out []audit.Entry
	sc2 := bufio.NewScanner(rc)
	// Audit lines are short, but a stack trace in the same stream is not, and
	// the default 64K token limit would end the scan at the first long one.
	sc2.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanned := 0; sc2.Scan() && scanned < maxScanned; scanned++ {
		e, ok := audit.Parse(sc2.Bytes())
		if !ok || e.Time.Before(from) || e.Time.After(to) || !keep(e, sc, f) {
			continue
		}
		out = append(out, e)
	}
	return out, sc2.Err()
}
