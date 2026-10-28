package kube

import (
	"context"
	"reflect"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

const testNS = "payments-dev"

func service(name string, mutate func(*corev1.Service)) *corev1.Service {
	s := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNS},
		Spec: corev1.ServiceSpec{
			Type:      corev1.ServiceTypeClusterIP,
			ClusterIP: "10.0.0.10",
			Selector:  map[string]string{"app": name},
			Ports:     []corev1.ServicePort{{Port: 8080, TargetPort: intstr.FromInt32(8080), Protocol: corev1.ProtocolTCP}},
		},
	}
	if mutate != nil {
		mutate(s)
	}
	return s
}

type ep struct {
	pod   string
	ready *bool
}

func slice(name, svc string, family discoveryv1.AddressType, eps ...ep) *discoveryv1.EndpointSlice {
	s := &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: testNS,
			Labels: map[string]string{discoveryv1.LabelServiceName: svc},
		},
		AddressType: family,
	}
	for _, e := range eps {
		s.Endpoints = append(s.Endpoints, discoveryv1.Endpoint{
			Addresses:  []string{"addr-" + e.pod + "-" + string(family)},
			Conditions: discoveryv1.EndpointConditions{Ready: e.ready},
			TargetRef:  &corev1.ObjectReference{Kind: "Pod", Name: e.pod, UID: types.UID("uid-" + e.pod)},
		})
	}
	return s
}

func yes() *bool { b := true; return &b }
func no() *bool  { b := false; return &b }

func byName(t *testing.T, objects ...runtime.Object) map[string]Service {
	t.Helper()
	got, err := listServices(context.Background(), fake.NewSimpleClientset(objects...), testNS)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]Service{}
	for _, s := range got {
		out[s.Name] = s
	}
	return out
}

func TestPortsAreReportedWithWhereThePodListens(t *testing.T) {
	http := "http"
	got := byName(t, service("api", func(s *corev1.Service) {
		s.Spec.Ports = []corev1.ServicePort{
			{Name: "http", Port: 8080, TargetPort: intstr.FromInt32(8081), Protocol: corev1.ProtocolTCP, AppProtocol: &http},
			{Name: "metrics", Port: 9090, TargetPort: intstr.FromString("metrics"), Protocol: corev1.ProtocolTCP},
		}
	}))["api"]
	if len(got.Ports) != 2 {
		t.Fatalf("ports = %+v", got.Ports)
	}
	if p := got.Ports[0]; p.Port != 8080 || p.TargetPort != "8081" || p.AppProtocol != "http" {
		t.Errorf("first port = %+v", p)
	}
	if p := got.Ports[1]; p.TargetPort != "metrics" {
		t.Errorf("a named target port came back as %q", p.TargetPort)
	}
}

func TestReadyPodsAreCounted(t *testing.T) {
	got := byName(t,
		service("api", nil),
		slice("api-a", "api", discoveryv1.AddressTypeIPv4, ep{"p1", yes()}, ep{"p2", no()}, ep{"p3", yes()}),
	)["api"]
	if got.Endpoints == nil || *got.Endpoints != (Endpoints{Ready: 2, Total: 3}) {
		t.Errorf("endpoints = %+v", got.Endpoints)
	}
}

// A dual-stack cluster lists every pod twice, once per address family. Counted
// by record, a service with two pods would claim four.
func TestAPodInBothFamiliesIsCountedOnce(t *testing.T) {
	got := byName(t,
		service("api", nil),
		slice("api-v4", "api", discoveryv1.AddressTypeIPv4, ep{"p1", yes()}, ep{"p2", yes()}),
		slice("api-v6", "api", discoveryv1.AddressTypeIPv6, ep{"p1", yes()}, ep{"p2", yes()}),
	)["api"]
	if got.Endpoints == nil || *got.Endpoints != (Endpoints{Ready: 2, Total: 2}) {
		t.Errorf("endpoints = %+v, want 2 of 2", got.Endpoints)
	}
}

// The API asks for an unstated readiness to be read as ready.
func TestUnstatedReadinessCountsAsReady(t *testing.T) {
	got := byName(t,
		service("api", nil),
		slice("api-a", "api", discoveryv1.AddressTypeIPv4, ep{"p1", nil}),
	)["api"]
	if got.Endpoints == nil || got.Endpoints.Ready != 1 {
		t.Errorf("endpoints = %+v", got.Endpoints)
	}
}

// A selector that matches nothing: the address exists and nothing answers.
// That is a known zero, not an unknown — the page says so.
func TestASelectorMatchingNothingIsAKnownZero(t *testing.T) {
	got := byName(t, service("legacy", nil))["legacy"]
	if !got.HasSelector || got.Endpoints == nil || *got.Endpoints != (Endpoints{}) {
		t.Errorf("got %+v, endpoints %+v", got, got.Endpoints)
	}
}

func TestHeadlessAndExternalNameAreToldApart(t *testing.T) {
	got := byName(t,
		service("pg", func(s *corev1.Service) { s.Spec.ClusterIP = corev1.ClusterIPNone }),
		service("partner", func(s *corev1.Service) {
			s.Spec.Type = corev1.ServiceTypeExternalName
			s.Spec.ExternalName = "gw.partner.example"
			s.Spec.ClusterIP = ""
			s.Spec.Selector = nil
			s.Spec.Ports = nil
		}),
	)
	if !got["pg"].Headless {
		t.Error("a service with clusterIP None is not reported as headless")
	}
	p := got["partner"]
	if p.Type != "ExternalName" || p.ExternalName != "gw.partner.example" {
		t.Errorf("ExternalName service = %+v", p)
	}
	// Nothing in the cluster stands behind an alias for an outside host, so
	// there is nothing to count — and "0 ready" would be a false alarm.
	if p.Endpoints != nil {
		t.Errorf("an ExternalName service was given endpoints: %+v", p.Endpoints)
	}
}

// A role without access to EndpointSlices still gets the addresses, which are
// the point of the page. The counts are unknown — absent, not zero.
func TestUnreadableEndpointsLeaveTheAddresses(t *testing.T) {
	cs := fake.NewSimpleClientset(service("api", nil))
	cs.PrependReactor("list", "endpointslices", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: "discovery.k8s.io", Resource: "endpointslices"}, "", nil)
	})
	got, err := listServices(context.Background(), cs, testNS)
	if err != nil {
		t.Fatalf("the listing failed because the counts could not be read: %v", err)
	}
	if len(got) != 1 || got[0].Endpoints != nil {
		t.Errorf("got %+v", got)
	}
}

func TestTheClusterDomainIsReadFromResolvConf(t *testing.T) {
	cases := map[string]string{
		"search payments-dev.svc.cluster.local svc.cluster.local cluster.local\nnameserver 10.96.0.10\n":       "cluster.local",
		"nameserver 10.0.0.10\nsearch ns.svc.corp.internal svc.corp.internal corp.internal\noptions ndots:5\n": "corp.internal",
		"nameserver 8.8.8.8\n": "cluster.local", // outside a cluster
		"":                     "cluster.local",
	}
	for in, want := range cases {
		if got := clusterDomainFrom(in); got != want {
			t.Errorf("clusterDomainFrom(%q) = %q, want %q", in, got, want)
		}
	}
}

func deployment(name string, podLabels map[string]string, ports ...corev1.ContainerPort) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNS},
		Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{Labels: podLabels},
			Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Ports: ports}}},
		}},
	}
}

func TestTheReleaseBehindAServiceIsNamed(t *testing.T) {
	got := byName(t, service("api", func(s *corev1.Service) {
		s.Annotations = map[string]string{"meta.helm.sh/release-name": "payments"}
		s.Labels = map[string]string{"helm.sh/chart": "payments-2.1.0"}
	}))["api"]
	if got.Release != "payments" || got.Chart != "payments-2.1.0" {
		t.Errorf("release, chart = %q, %q", got.Release, got.Chart)
	}
}

func TestATargetPortThePodsDoNotDeclareIsReported(t *testing.T) {
	svc := func(target intstr.IntOrString) *corev1.Service {
		return service("api", func(s *corev1.Service) {
			s.Spec.Ports = []corev1.ServicePort{{Port: 80, TargetPort: target, Protocol: corev1.ProtocolTCP}}
		})
	}
	pods := map[string]string{"app": "api"}
	http := corev1.ContainerPort{Name: "http", ContainerPort: 8080}

	cases := []struct {
		name   string
		target intstr.IntOrString
		dep    *appsv1.Deployment
		want   []string
	}{
		{"number not declared", intstr.FromInt32(3000), deployment("api", pods, http), []string{"http 8080"}},
		{"number declared", intstr.FromInt32(8080), deployment("api", pods, http), nil},
		{"name declared", intstr.FromString("http"), deployment("api", pods, http), nil},
		{"name not declared", intstr.FromString("web"), deployment("api", pods, http), []string{"http 8080"}},
		// Declaring ports is optional, and pods that declare none say nothing.
		{"nothing declared", intstr.FromInt32(3000), deployment("api", pods), nil},
		{"no workload found", intstr.FromInt32(3000), deployment("other", map[string]string{"app": "other"}, http), nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := byName(t, svc(c.target), c.dep)["api"].Ports[0].Declared
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("declared = %v, want %v", got, c.want)
			}
		})
	}
}

func TestThePodsBehindAServiceAreNamed(t *testing.T) {
	got := byName(t, service("api", nil),
		slice("api-v4", "api", discoveryv1.AddressTypeIPv4, ep{pod: "api-b", ready: yes()}, ep{pod: "api-a", ready: no()}),
		slice("api-v6", "api", discoveryv1.AddressTypeIPv6, ep{pod: "api-b", ready: yes()}),
	)["api"]
	if want := []string{"api-a", "api-b"}; !reflect.DeepEqual(got.Pods, want) {
		t.Errorf("pods = %v, want %v", got.Pods, want)
	}
}
