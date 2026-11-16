package kube

import (
	"sync"
	"time"

	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/memory"
)

// The cluster's kinds, as the portal's own account reads them, are kept for a
// few minutes: reading them is a request per API group, and checking a chart
// from the catalog needs them on every keystroke's check. Helm asks for a
// fresh copy each time it starts an action; with the portal's account it gets
// the kept one instead, and a kind the copy does not know sends the plan back
// for a fresh one.

const discoveryTTL = 5 * time.Minute

type discoveryCache struct {
	mu sync.Mutex
	dc discovery.CachedDiscoveryInterface
	at time.Time
}

// keptDiscovery is a discovery cache Helm cannot empty: it is renewed on its
// own schedule.
type keptDiscovery struct {
	discovery.CachedDiscoveryInterface
}

func (keptDiscovery) Invalidate() {}

// portalDiscovery is the kept discovery of the portal's own account.
func (m *Manager) portalDiscovery() (discovery.CachedDiscoveryInterface, error) {
	m.disc.mu.Lock()
	defer m.disc.mu.Unlock()
	if m.disc.dc != nil && time.Since(m.disc.at) < discoveryTTL {
		return m.disc.dc, nil
	}
	dc, err := discovery.NewDiscoveryClientForConfig(m.restConfig(""))
	if err != nil {
		return nil, err
	}
	m.disc.dc = keptDiscovery{memory.NewMemCacheClient(dc)}
	m.disc.at = time.Now()
	return m.disc.dc, nil
}

// forgetDiscovery drops the kept copy: a kind was asked for that it lacks.
func (m *Manager) forgetDiscovery() {
	m.disc.mu.Lock()
	defer m.disc.mu.Unlock()
	m.disc.dc = nil
}

// portalGetter is a REST client getter for the portal's own account that
// shares the kept discovery.
func (m *Manager) portalGetter(namespace string) *restClientGetter {
	g := newRESTClientGetter(m.restConfig(""), namespace)
	if dc, err := m.portalDiscovery(); err == nil {
		g.disc = dc
	}
	return g
}
