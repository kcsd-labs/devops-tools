package kube

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes/fake"
)

func certPEM(t *testing.T, notAfter time.Time, names ...string) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: names[0]},
		DNSNames: names, NotBefore: time.Now().Add(-time.Hour), NotAfter: notAfter}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func tlsSecret(t *testing.T, ns, name string, notAfter time.Time, names ...string) *corev1.Secret {
	return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}, Type: corev1.SecretTypeTLS,
		Data: map[string][]byte{corev1.TLSCertKey: certPEM(t, notAfter, names...), corev1.TLSPrivateKeyKey: []byte("k")}}
}

func ingObj(ns, name string, created time.Time, host, path, svc string, port int32, tls string) *networkingv1.Ingress {
	pt := networkingv1.PathTypePrefix
	ing := &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, CreationTimestamp: metav1.NewTime(created),
			ResourceVersion: "1", Annotations: map[string]string{"meta.helm.sh/release-name": name,
				"nginx.ingress.kubernetes.io/proxy-read-timeout": "3600"}},
		Spec: networkingv1.IngressSpec{Rules: []networkingv1.IngressRule{{Host: host,
			IngressRuleValue: networkingv1.IngressRuleValue{HTTP: &networkingv1.HTTPIngressRuleValue{
				Paths: []networkingv1.HTTPIngressPath{{Path: path, PathType: &pt, Backend: networkingv1.IngressBackend{
					Service: &networkingv1.IngressServiceBackend{Name: svc, Port: networkingv1.ServiceBackendPort{Number: port}}}}}}}}}},
		Status: networkingv1.IngressStatus{LoadBalancer: networkingv1.IngressLoadBalancerStatus{
			Ingress: []networkingv1.IngressLoadBalancerIngress{{IP: "10.0.0.1"}}}},
	}
	if tls != "" {
		ing.Spec.TLS = []networkingv1.IngressTLS{{SecretName: tls, Hosts: []string{host}}}
	}
	return ing
}

func svcObj(ns, name string, ports ...corev1.ServicePort) *corev1.Service {
	return &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}, Spec: corev1.ServiceSpec{Ports: ports}}
}

func epSlice(ns, svc string, ready bool) *discoveryv1.EndpointSlice {
	return &discoveryv1.EndpointSlice{ObjectMeta: metav1.ObjectMeta{Name: svc + "-x", Namespace: ns,
		Labels: map[string]string{discoveryv1.LabelServiceName: svc}},
		Endpoints: []discoveryv1.Endpoint{{Addresses: []string{"10.1.0.1"}, Conditions: discoveryv1.EndpointConditions{Ready: &ready},
			TargetRef: &corev1.ObjectReference{Kind: "Pod", Name: svc + "-pod"}}}}
}

func ingressWorldFor(t *testing.T) (*ingressWorld, *fake.Clientset) {
	now := time.Now()
	long := now.AddDate(1, 0, 0)
	objs := []runtime.Object{
		&networkingv1.IngressClass{ObjectMeta: metav1.ObjectMeta{Name: "nginx",
			Annotations: map[string]string{networkingv1.AnnotationIsDefaultIngressClass: "true"}}},
		svcObj("abs-dev", "abs-api", corev1.ServicePort{Name: "http", Port: 8080}),
		svcObj("abs-dev", "abs-plus", corev1.ServicePort{Name: "http", Port: 8080}),
		svcObj("abs-dev", "abs-reports", corev1.ServicePort{Name: "web", Port: 8080}),
		svcObj("abs-dev", "abs-admin", corev1.ServicePort{Port: 80}),
		epSlice("abs-dev", "abs-api", true), epSlice("abs-dev", "abs-plus", true), epSlice("abs-dev", "abs-admin", false), epSlice("abs-dev", "abs-reports", true),
		tlsSecret(t, "abs-dev", "wildcard-tls", long, "*.dev.example.com"),
		tlsSecret(t, "abs-dev", "old-tls", now.AddDate(0, 0, 9), "*.dev.example.com"),
		ingObj("abs-dev", "abs-api", now.Add(-48*time.Hour), "abs-api.dev.example.com", "/", "abs-api", 8080, "wildcard-tls"),
		ingObj("abs-dev", "abs-plus", now.Add(-48*time.Hour), "abs-plus.dev.example.com", "/", "abs-plus", 9090, "wildcard-tls"),
		ingObj("abs-dev", "abs-admin", now.Add(-48*time.Hour), "abs-admin.dev.example.com", "/", "abs-admin", 80, ""),
		ingObj("abs-dev", "reports", now.Add(-48*time.Hour), "reports.dev.example.com", "/", "abs-reports", 8080, "old-tls"),
		ingObj("abs-dev", "bill", now.Add(-24*time.Hour), "billing.dev.example.com", "/", "abs-api", 8080, ""),
		ingObj("billing-dev", "billing", now.Add(-72*time.Hour), "billing.dev.example.com", "/", "billing", 8080, ""),
	}
	cs := fake.NewSimpleClientset(objs...)
	return buildWorld(context.Background(), cs, cs, "abs-dev"), cs
}

func problemsOf(t *testing.T, w *ingressWorld, cs *fake.Clientset, name string) []IngressProblem {
	t.Helper()
	ing, err := cs.NetworkingV1().Ingresses("abs-dev").Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	rs := w.routes(ing)
	if len(rs) != 1 {
		t.Fatalf("%s: %d routes", name, len(rs))
	}
	return rs[0].Problems
}

// Each problem the page names comes with what to do about it.
func TestIngressProblemsComeWithTheirFix(t *testing.T) {
	w, cs := ingressWorldFor(t)

	if p := problemsOf(t, w, cs, "abs-api"); len(p) != 0 {
		t.Errorf("abs-api should work: %+v", p)
	}
	p := problemsOf(t, w, cs, "abs-plus")
	if len(p) != 1 || !strings.Contains(p[0].Text, "no port 9090") || p[0].Fix == nil || p[0].Fix.Value != "8080" {
		t.Errorf("abs-plus: %+v", p)
	}
	p = problemsOf(t, w, cs, "abs-admin")
	if len(p) != 1 || p[0].Text != "0/1 pods ready" || p[0].Fix.Kind != "pods" {
		t.Errorf("abs-admin: %+v", p)
	}
	p = problemsOf(t, w, cs, "reports")
	if len(p) != 1 || !strings.Contains(p[0].Text, "old-tls ends in") || p[0].Fix == nil || p[0].Fix.Value != "wildcard-tls" {
		t.Errorf("reports: %+v", p)
	}
	p = problemsOf(t, w, cs, "bill")
	if len(p) != 1 || p[0].Level != "error" || !strings.Contains(p[0].Text, "older ingress billing in billing-dev") ||
		p[0].Fix.Namespace != "billing-dev" {
		t.Errorf("bill: %+v", p)
	}
}

// A new ingress for a service is filled in from what is around it.
func TestASuggestionComesFromTheNeighbours(t *testing.T) {
	w, _ := ingressWorldFor(t)
	s := w.suggest(w.services["abs-reports"], "abs-dev")
	if s.Host != "abs-reports.dev.example.com" || s.TLS != "wildcard-tls" || s.Class != "nginx" || s.Port != "8080" {
		t.Errorf("suggestion: %+v", s)
	}
	if len(s.Secrets) == 0 || s.Secrets[0] != "wildcard-tls" {
		t.Errorf("the covering certificate should be offered first: %v", s.Secrets)
	}
}

func TestEditingAnIngressKeepsWhatItsInstallerOwns(t *testing.T) {
	w, cs := ingressWorldFor(t)
	ctx := context.Background()
	f := IngressForm{Class: "nginx",
		Rules: []IngressRule{
			{Host: "abs-plus.dev.example.com", Path: "/", Service: "abs-plus", Port: "8080"},
			{Host: "abs-plus.dev.example.com", Path: "/admin", Service: "abs-admin", Port: "80"},
		},
		TLS:         []IngressTLS{{Secret: "wildcard-tls", Hosts: []string{"abs-plus.dev.example.com"}}},
		Annotations: map[string]string{"nginx.ingress.kubernetes.io/proxy-body-size": "50m"},
	}
	if _, _, err := updateIngress(ctx, cs, w, "abs-dev", "abs-plus", "0", IngressChange{Form: &f}); !errors.Is(err, ErrIngressChanged) {
		t.Fatalf("a stale version should be refused: %v", err)
	}
	doc, detail, err := updateIngress(ctx, cs, w, "abs-dev", "abs-plus", "1", IngressChange{Form: &f})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"abs-plus.dev.example.com/ → abs-plus:8080 (was abs-plus:9090)",
		"added abs-plus.dev.example.com/admin → abs-admin:80", "class nginx",
		"settings changed: nginx.ingress.kubernetes.io/proxy-body-size, nginx.ingress.kubernetes.io/proxy-read-timeout"} {
		if !strings.Contains(detail, want) {
			t.Errorf("detail %q lacks %q", detail, want)
		}
	}
	saved, _ := cs.NetworkingV1().Ingresses("abs-dev").Get(ctx, "abs-plus", metav1.GetOptions{})
	if saved.Annotations["meta.helm.sh/release-name"] != "abs-plus" {
		t.Error("Helm's own annotation was dropped")
	}
	if len(saved.Spec.Rules) != 1 || len(saved.Spec.Rules[0].HTTP.Paths) != 2 {
		t.Errorf("two paths under one host expected: %+v", saved.Spec.Rules)
	}
	if doc.Annotations["meta.helm.sh/release-name"] != "" {
		t.Error("Helm's annotation should not be offered for editing")
	}
}

func TestIngressYAMLRoundTrip(t *testing.T) {
	w, cs := ingressWorldFor(t)
	ctx := context.Background()
	ing, _ := cs.NetworkingV1().Ingresses("abs-dev").Get(ctx, "abs-api", metav1.GetOptions{})
	text := renderIngress(ing)
	if strings.Contains(text, "meta.helm.sh") || !strings.Contains(text, "proxy-read-timeout") {
		t.Errorf("YAML:\n%s", text)
	}
	text = strings.Replace(text, `"3600"`, `"60"`, 1)
	_, detail, err := updateIngress(ctx, cs, w, "abs-dev", "abs-api", "1", IngressChange{YAML: &text})
	if err != nil {
		t.Fatal(err)
	}
	if detail != "settings changed: nginx.ingress.kubernetes.io/proxy-read-timeout" {
		t.Errorf("detail %q", detail)
	}
	bad := "spec: {}\nstatus: {}\n"
	if _, _, err := updateIngress(ctx, cs, w, "abs-dev", "abs-api", "1", IngressChange{YAML: &bad}); err == nil {
		t.Error("anything but annotations and spec should be refused")
	}
}

func TestCertificateCoverage(t *testing.T) {
	c := &certInfo{names: []string{"*.dev.example.com", "dev.example.com"}}
	for host, want := range map[string]bool{
		"abs.dev.example.com": true, "dev.example.com": true, "a.b.dev.example.com": false, "abs.preprod.example.com": false,
	} {
		if c.covers(host) != want {
			t.Errorf("covers(%s) = %v", host, !want)
		}
	}
}

// What ingress-nginx serves without complaint is not reported: no class, no
// address written back, a backend naming the pods' port.
func TestWhatTheControllerServesIsNotAProblem(t *testing.T) {
	w, cs := ingressWorldFor(t)
	ctx := context.Background()
	svc := svcObj("abs-dev", "abs-ui", corev1.ServicePort{Name: "http", Port: 80, TargetPort: intstr.FromInt32(8080)})
	if _, err := cs.CoreV1().Services("abs-dev").Create(ctx, svc, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	w = buildWorld(ctx, cs, cs, "abs-dev")
	w.ready["abs-ui"] = behind{Endpoints: Endpoints{Ready: 1, Total: 1}}
	ing := ingObj("abs-dev", "ui", time.Now().Add(-48*time.Hour), "ui.dev.example.com", "/", "abs-ui", 8080, "")
	ing.Status = networkingv1.IngressStatus{}
	w.defaultClass = ""
	if rs := w.routes(ing); len(rs[0].Problems) != 0 {
		t.Errorf("served by ingress-nginx, yet: %+v", rs[0].Problems)
	}
	ing.Spec.TLS = []networkingv1.IngressTLS{{SecretName: "gone-tls", Hosts: []string{"ui.dev.example.com"}}}
	p := w.routes(ing)[0].Problems
	if len(p) != 1 || p[0].Level != "warn" || !strings.Contains(p[0].Text, "default certificate") {
		t.Errorf("a missing secret: %+v", p)
	}
}

// A route with nothing wrong is sent with an empty list of problems, not null:
// the page counts them, and null took the whole page down.
func TestARouteWithoutProblemsSendsAnEmptyList(t *testing.T) {
	w, cs := ingressWorldFor(t)
	ing, _ := cs.NetworkingV1().Ingresses("abs-dev").Get(context.Background(), "abs-api", metav1.GetOptions{})
	b, err := json.Marshal(w.routes(ing))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"problems":[]`) {
		t.Errorf("routes: %s", b)
	}
}
