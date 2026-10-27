package kube

import (
	"context"
	"os"
	"sort"
	"strings"
	"sync"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// Service is a Service as the Services page shows it: where it answers, not how
// it is specified.
type Service struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	// Type is ClusterIP, NodePort, LoadBalancer or ExternalName.
	Type string `json:"type"`
	// Headless services have no address of their own: the name resolves
	// straight to the pods, and nothing balances in front of them.
	Headless bool `json:"headless"`
	// ExternalName is the outside host an ExternalName service is an alias
	// for. Looks like a service in the cluster, answers outside it.
	ExternalName string        `json:"externalName,omitempty"`
	Ports        []ServicePort `json:"ports"`
	// HasSelector separates a service whose selector matches no pod from one
	// that has no selector at all, whose endpoints somebody manages by hand.
	HasSelector bool `json:"hasSelector"`
	// Endpoints counts the pods behind the service. Nil when they could not be
	// read: unknown is not the same as none, and the page must not say "0
	// ready" about a service it simply could not look behind.
	Endpoints *Endpoints `json:"endpoints,omitempty"`
}

// ServicePort is one port a service answers on.
type ServicePort struct {
	Name string `json:"name,omitempty"`
	Port int32  `json:"port"`
	// TargetPort is where the pod listens: a number, or the name of a port in
	// the pod spec. Shown beside Port because the two differing — and one of
	// them being wrong — is the commonest reason a call goes nowhere.
	TargetPort  string `json:"targetPort"`
	Protocol    string `json:"protocol"`
	AppProtocol string `json:"appProtocol,omitempty"`
	NodePort    int32  `json:"nodePort,omitempty"`
}

// Endpoints is how many pods are behind a service, and how many of them may
// receive traffic.
type Endpoints struct {
	Ready int `json:"ready"`
	Total int `json:"total"`
}

// ListServices returns the services in one namespace, each with a count of the
// pods behind it.
func (m *Manager) ListServices(ctx context.Context, asUser, namespace string) ([]Service, error) {
	cs, err := m.Clientset(asUser)
	if err != nil {
		return nil, err
	}
	return listServices(ctx, cs, namespace)
}

func listServices(ctx context.Context, cs kubernetes.Interface, namespace string) ([]Service, error) {
	list, err := cs.CoreV1().Services(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}

	// The pods behind each service, read in one call for the namespace rather
	// than one per service. Not fatal when it fails — a role without access to
	// EndpointSlices still gets the addresses, which are the point of the page.
	counts, cerr := endpointCounts(ctx, cs, namespace)

	out := make([]Service, 0, len(list.Items))
	for _, s := range list.Items {
		svc := Service{
			Name:         s.Name,
			Namespace:    s.Namespace,
			Type:         string(s.Spec.Type),
			Headless:     s.Spec.ClusterIP == corev1.ClusterIPNone,
			ExternalName: s.Spec.ExternalName,
			HasSelector:  len(s.Spec.Selector) > 0,
			Ports:        make([]ServicePort, 0, len(s.Spec.Ports)),
		}
		if svc.Type == "" {
			svc.Type = string(corev1.ServiceTypeClusterIP)
		}
		for _, p := range s.Spec.Ports {
			sp := ServicePort{
				Name:       p.Name,
				Port:       p.Port,
				TargetPort: p.TargetPort.String(),
				Protocol:   string(p.Protocol),
				NodePort:   p.NodePort,
			}
			if p.AppProtocol != nil {
				sp.AppProtocol = *p.AppProtocol
			}
			svc.Ports = append(svc.Ports, sp)
		}
		// An ExternalName service has nothing behind it in the cluster to count.
		if cerr == nil && s.Spec.Type != corev1.ServiceTypeExternalName {
			e := counts[s.Name]
			svc.Endpoints = &e
		}
		out = append(out, svc)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// endpointCounts counts the pods behind every service in a namespace.
//
// By pod, not by endpoint record: a dual-stack cluster gives every pod one
// entry in an IPv4 slice and another in an IPv6 slice, and counting records
// would report twice the pods there are. An endpoint whose readiness is not
// stated counts as ready, as the API asks consumers to treat it.
func endpointCounts(ctx context.Context, cs kubernetes.Interface, namespace string) (map[string]Endpoints, error) {
	slices, err := cs.DiscoveryV1().EndpointSlices(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	type seen struct{ ready bool }
	pods := map[string]map[string]seen{} // service -> pod -> readiness
	for _, sl := range slices.Items {
		svc := sl.Labels[discoveryv1.LabelServiceName]
		if svc == "" {
			continue
		}
		if pods[svc] == nil {
			pods[svc] = map[string]seen{}
		}
		for _, ep := range sl.Endpoints {
			key := endpointKey(ep)
			if key == "" {
				continue
			}
			ready := ep.Conditions.Ready == nil || *ep.Conditions.Ready
			// Ready in either family is ready.
			pods[svc][key] = seen{ready: ready || pods[svc][key].ready}
		}
	}
	out := make(map[string]Endpoints, len(pods))
	for svc, byPod := range pods {
		var e Endpoints
		for _, p := range byPod {
			e.Total++
			if p.ready {
				e.Ready++
			}
		}
		out[svc] = e
	}
	return out, nil
}

// endpointKey names the pod an endpoint belongs to, or failing that its first
// address.
func endpointKey(ep discoveryv1.Endpoint) string {
	if ep.TargetRef != nil && ep.TargetRef.UID != "" {
		return string(ep.TargetRef.UID)
	}
	if len(ep.Addresses) > 0 {
		return ep.Addresses[0]
	}
	return ""
}

var (
	clusterDomainOnce sync.Once
	clusterDomain     string
)

// ClusterDomain is the suffix of fully qualified service names — cluster.local
// unless the cluster was built otherwise.
//
// Read from this pod's own resolv.conf, where the kubelet writes the cluster's
// search domains: <namespace>.svc.<domain>, svc.<domain>, <domain>. A fixed
// "cluster.local" would print addresses that do not resolve on the clusters
// where it matters that they should.
func ClusterDomain() string {
	clusterDomainOnce.Do(func() {
		b, _ := os.ReadFile("/etc/resolv.conf")
		clusterDomain = clusterDomainFrom(string(b))
	})
	return clusterDomain
}

func clusterDomainFrom(resolvConf string) string {
	for _, line := range strings.Split(resolvConf, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || fields[0] != "search" {
			continue
		}
		for _, f := range fields[1:] {
			if d, ok := strings.CutPrefix(f, "svc."); ok && d != "" {
				return d
			}
		}
	}
	return "cluster.local"
}
