package kube

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/yaml"
)

// The Ingress page answers one question — can this be reached from outside,
// and if not, why — and offers the fix beside each answer. Nothing is
// restricted here beyond what a role grants: no list of allowed hosts or
// annotations is written anywhere. What the page knows, it learns from the
// cluster: the hosts its neighbours use, the certificates in the namespace,
// the class marked default.

var (
	ErrIngressChanged = errors.New("someone changed this ingress since you opened it — reload to see their changes, then make yours again")
)

// certWarnDays is how close to its end a certificate starts being mentioned.
const certWarnDays = 14

// IngressRoute is one host and path an ingress answers, as the list shows it.
type IngressRoute struct {
	Ingress string `json:"ingress"`
	Host    string `json:"host"` // "" for the default backend: any host
	Path    string `json:"path"`
	Service string `json:"service"`
	Port    string `json:"port"`
	// Ready counts the pods behind the service; nil when unknown.
	Ready     *Endpoints `json:"ready,omitempty"`
	TLSSecret string     `json:"tlsSecret,omitempty"`
	// TLSDays is how long the certificate has left; nil without one.
	TLSDays  *int             `json:"tlsDays,omitempty"`
	Class    string           `json:"class,omitempty"`
	Release  string           `json:"release,omitempty"`
	Problems []IngressProblem `json:"problems"`
}

// IngressProblem is what stands between a route and working, and what to do.
type IngressProblem struct {
	Level string      `json:"level"` // "warn" or "error"
	Text  string      `json:"text"`
	Fix   *IngressFix `json:"fix,omitempty"`
}

// IngressFix is the action offered beside a problem.
type IngressFix struct {
	// Kind is "port" (use Value as the port), "tls" (use secret Value),
	// "pods" (look at the service's pods) or "ingress" (open the ingress in
	// Namespace/Name that holds the address).
	Kind      string `json:"kind"`
	Value     string `json:"value,omitempty"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name,omitempty"`
}

// IngressList is the namespace's routes, and what the cluster suggests.
type IngressList struct {
	Routes []IngressRoute `json:"routes"`
	// AnnotationKeys are the annotations used on ingresses anywhere in the
	// cluster: suggestions for the settings field, not a limit.
	AnnotationKeys []string `json:"annotationKeys"`
	Classes        []string `json:"classes"`
}

// IngressRule is one host, path and backend, as the form edits it.
type IngressRule struct {
	Host     string `json:"host"`
	Path     string `json:"path"`
	PathType string `json:"pathType,omitempty"`
	Service  string `json:"service"`
	Port     string `json:"port"`
}

// IngressTLS is one certificate and the hosts it is used for.
type IngressTLS struct {
	Secret string   `json:"secret"`
	Hosts  []string `json:"hosts"`
}

// IngressForm is an ingress as the form edits it.
type IngressForm struct {
	Class       string            `json:"class"`
	Rules       []IngressRule     `json:"rules"`
	TLS         []IngressTLS      `json:"tls"`
	Annotations map[string]string `json:"annotations"`
}

// IngressDoc is one ingress opened for editing.
type IngressDoc struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Release string `json:"release,omitempty"`
	IngressForm
	// YAML is the editable part, as kubectl would show it: annotations and spec.
	YAML string `json:"yaml"`
	// DefaultBackend is set when the ingress has one; the form keeps it as it is.
	DefaultBackend string         `json:"defaultBackend,omitempty"`
	Routes         []IngressRoute `json:"routes"`
}

// hiddenAnnotation marks what the tools that installed an ingress keep on it.
// Not shown, not changed: editing Helm's own bookkeeping breaks the release.
func hiddenAnnotation(k string) bool {
	return strings.HasPrefix(k, "meta.helm.sh/") || strings.HasPrefix(k, "kubectl.kubernetes.io/")
}

// --- what the cluster knows ------------------------------------------------

type certInfo struct {
	names    []string
	notAfter time.Time
}

func (c *certInfo) covers(host string) bool {
	for _, n := range c.names {
		if n == host {
			return true
		}
		if strings.HasPrefix(n, "*.") {
			rest, ok := strings.CutPrefix(host, firstLabel(host)+".")
			if ok && rest == n[2:] {
				return true
			}
		}
	}
	return false
}

func (c *certInfo) daysLeft(now time.Time) int {
	return int(c.notAfter.Sub(now).Hours() / 24)
}

func firstLabel(host string) string {
	if i := strings.IndexByte(host, '.'); i >= 0 {
		return host[:i]
	}
	return host
}

func parseCert(sec *corev1.Secret) *certInfo {
	block, _ := pem.Decode(sec.Data[corev1.TLSCertKey])
	if block == nil {
		return nil
	}
	c, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil
	}
	names := append([]string(nil), c.DNSNames...)
	if len(names) == 0 && c.Subject.CommonName != "" {
		names = []string{c.Subject.CommonName}
	}
	return &certInfo{names: names, notAfter: c.NotAfter}
}

type claim struct {
	namespace, name string
	created         time.Time
}

// ingressWorld is what the routes of one namespace are checked against.
type ingressWorld struct {
	now      time.Time
	services map[string]corev1.Service
	ready    map[string]behind // nil when the pods could not be counted
	// certs are the TLS secrets of the namespace; a name mapped to nil exists
	// but holds no certificate that parses.
	certs map[string]*certInfo
	// claims are every ingress in the cluster that answers a host and path.
	claims       map[string][]claim
	classes      map[string]bool // nil when unknown
	defaultClass string
	all          []networkingv1.Ingress // the whole cluster's, for suggestions
}

func claimKey(host, path string) string {
	if path == "" {
		path = "/"
	}
	return host + "\x00" + path
}

// world reads what the checks need. The namespace's own objects are read as the
// person; certificates and the rest of the cluster through the portal's own
// account, because they are read for their metadata only — a certificate's
// names and dates, another namespace's hosts — and showing those is the point.
func (m *Manager) world(ctx context.Context, asUser, namespace string) (*ingressWorld, kubernetes.Interface, error) {
	cs, err := m.Clientset(asUser)
	if err != nil {
		return nil, nil, err
	}
	own, err := m.Clientset("")
	if err != nil {
		return nil, nil, err
	}
	return buildWorld(ctx, cs, own, namespace), cs, nil
}

func buildWorld(ctx context.Context, cs, own kubernetes.Interface, namespace string) *ingressWorld {
	w := &ingressWorld{now: time.Now(), services: map[string]corev1.Service{}, certs: map[string]*certInfo{},
		claims: map[string][]claim{}}
	if l, err := cs.CoreV1().Services(namespace).List(ctx, metav1.ListOptions{}); err == nil {
		for _, s := range l.Items {
			w.services[s.Name] = s
		}
	}
	if counts, err := endpointCounts(ctx, cs, namespace); err == nil {
		w.ready = counts
	}
	if l, err := own.CoreV1().Secrets(namespace).List(ctx, metav1.ListOptions{
		FieldSelector: "type=" + string(corev1.SecretTypeTLS),
	}); err == nil {
		for i := range l.Items {
			w.certs[l.Items[i].Name] = parseCert(&l.Items[i])
		}
	}
	if l, err := own.NetworkingV1().Ingresses("").List(ctx, metav1.ListOptions{}); err == nil {
		w.all = l.Items
		for _, ing := range l.Items {
			for _, r := range ing.Spec.Rules {
				if r.HTTP == nil {
					continue
				}
				for _, p := range r.HTTP.Paths {
					k := claimKey(r.Host, p.Path)
					w.claims[k] = append(w.claims[k], claim{ing.Namespace, ing.Name, ing.CreationTimestamp.Time})
				}
			}
		}
	}
	if l, err := own.NetworkingV1().IngressClasses().List(ctx, metav1.ListOptions{}); err == nil {
		w.classes = map[string]bool{}
		for _, c := range l.Items {
			w.classes[c.Name] = true
			if c.Annotations[networkingv1.AnnotationIsDefaultIngressClass] == "true" {
				w.defaultClass = c.Name
			}
		}
	}
	return w
}

// --- the checks ------------------------------------------------------------

func portOf(b *networkingv1.IngressServiceBackend) string {
	if b.Port.Name != "" {
		return b.Port.Name
	}
	return strconv.Itoa(int(b.Port.Number))
}

// servicePortFor finds the service port a backend names.
func servicePortFor(svc corev1.Service, port string) bool {
	for _, p := range svc.Spec.Ports {
		if p.Name == port || strconv.Itoa(int(p.Port)) == port {
			return true
		}
	}
	return false
}

// likelyPort is the port to offer for a service: the one named http, or the
// only one there is.
func likelyPort(svc corev1.Service) string {
	for _, p := range svc.Spec.Ports {
		if p.Name == "http" {
			return strconv.Itoa(int(p.Port))
		}
	}
	if len(svc.Spec.Ports) == 1 {
		return strconv.Itoa(int(svc.Spec.Ports[0].Port))
	}
	return ""
}

// bestCert is the certificate in the namespace that covers host and lasts
// longest, other than the one named.
func (w *ingressWorld) bestCert(host, except string) (string, *certInfo) {
	var name string
	var best *certInfo
	for n, c := range w.certs {
		if n == except || c == nil || !c.covers(host) || !c.notAfter.After(w.now) {
			continue
		}
		if best == nil || c.notAfter.After(best.notAfter) || (c.notAfter.Equal(best.notAfter) && n < name) {
			name, best = n, c
		}
	}
	return name, best
}

func classOf(ing *networkingv1.Ingress) string {
	if ing.Spec.IngressClassName != nil {
		return *ing.Spec.IngressClassName
	}
	return ing.Annotations["kubernetes.io/ingress.class"]
}

// routes lists what one ingress answers, each with what stands in its way.
func (w *ingressWorld) routes(ing *networkingv1.Ingress) []IngressRoute {
	class := classOf(ing)
	var common []IngressProblem
	if class != "" && w.classes != nil && !w.classes[class] {
		common = append(common, IngressProblem{Level: "error",
			Text: "no ingress class " + class + " in the cluster: no controller will serve it"})
	} else if class == "" && w.classes != nil && w.defaultClass == "" {
		common = append(common, IngressProblem{Level: "warn",
			Text: "no class, and none in the cluster is the default: a controller may not pick it up"})
	}
	if len(ing.Status.LoadBalancer.Ingress) == 0 && ing.CreationTimestamp.Time.Before(w.now.Add(-2*time.Minute)) {
		common = append(common, IngressProblem{Level: "warn",
			Text: "no address yet: the controller has not taken it up"})
	}

	tlsFor := map[string]string{}
	for _, t := range ing.Spec.TLS {
		for _, h := range t.Hosts {
			tlsFor[h] = t.SecretName
		}
	}

	var out []IngressRoute
	add := func(host, path string, be *networkingv1.IngressServiceBackend) {
		r := IngressRoute{Ingress: ing.Name, Host: host, Path: path, Class: class,
			Release: ing.Annotations["meta.helm.sh/release-name"]}
		if r.Path == "" {
			r.Path = "/"
		}
		r.Problems = append(r.Problems, common...)
		if be == nil {
			r.Problems = append(r.Problems, IngressProblem{Level: "error", Text: "no service behind this path"})
			out = append(out, r)
			return
		}
		r.Service, r.Port = be.Name, portOf(be)
		svc, ok := w.services[be.Name]
		switch {
		case !ok:
			r.Problems = append(r.Problems, IngressProblem{Level: "error",
				Text: "no service " + be.Name + " in this namespace"})
		case !servicePortFor(svc, r.Port):
			p := IngressProblem{Level: "error", Text: "service " + be.Name + " has no port " + r.Port}
			if lp := likelyPort(svc); lp != "" {
				p.Fix = &IngressFix{Kind: "port", Value: lp}
			}
			r.Problems = append(r.Problems, p)
		}
		if ok && w.ready != nil {
			b := w.ready[be.Name]
			e := b.Endpoints
			r.Ready = &e
			if e.Ready == 0 {
				r.Problems = append(r.Problems, IngressProblem{Level: "warn",
					Text: fmt.Sprintf("%d/%d pods ready", e.Ready, e.Total), Fix: &IngressFix{Kind: "pods", Name: be.Name}})
			}
		}
		if secret, ok := tlsFor[host]; ok && host != "" {
			r.TLSSecret = secret
			c, known := w.certs[secret]
			alt, _ := w.bestCert(host, secret)
			var fix *IngressFix
			if alt != "" {
				fix = &IngressFix{Kind: "tls", Value: alt}
			}
			switch {
			case !known:
				r.Problems = append(r.Problems, IngressProblem{Level: "error",
					Text: "no TLS secret " + secret + " in this namespace", Fix: fix})
			case c == nil:
				r.Problems = append(r.Problems, IngressProblem{Level: "error",
					Text: "the secret " + secret + " holds no readable certificate", Fix: fix})
			default:
				d := c.daysLeft(w.now)
				r.TLSDays = &d
				switch {
				case !c.notAfter.After(w.now):
					r.Problems = append(r.Problems, IngressProblem{Level: "error",
						Text: "certificate in " + secret + " has expired", Fix: fix})
				case !c.covers(host):
					r.Problems = append(r.Problems, IngressProblem{Level: "error",
						Text: "certificate in " + secret + " does not cover " + host, Fix: fix})
				case d <= certWarnDays:
					r.Problems = append(r.Problems, IngressProblem{Level: "warn",
						Text: fmt.Sprintf("certificate in %s ends in %d d", secret, d), Fix: fix})
				}
			}
		}
		r.Problems = append(r.Problems, w.conflicts(ing, host, path)...)
		out = append(out, r)
	}
	if ing.Spec.DefaultBackend != nil {
		add("", "/", ing.Spec.DefaultBackend.Service)
	}
	for _, rule := range ing.Spec.Rules {
		if rule.HTTP == nil {
			continue
		}
		for _, p := range rule.HTTP.Paths {
			add(rule.Host, p.Path, p.Backend.Service)
		}
	}
	return out
}

// conflicts reports another ingress answering the same host and path. Not an
// error to Kubernetes: ingress-nginx keeps the oldest and quietly ignores the
// rest, so the one that loses looks configured and receives nothing.
func (w *ingressWorld) conflicts(ing *networkingv1.Ingress, host, path string) []IngressProblem {
	if host == "" {
		return nil
	}
	others := w.claims[claimKey(host, path)]
	var older, newer []claim
	for _, c := range others {
		if c.namespace == ing.Namespace && c.name == ing.Name {
			continue
		}
		if c.created.Before(ing.CreationTimestamp.Time) || ing.CreationTimestamp.IsZero() {
			older = append(older, c)
		} else {
			newer = append(newer, c)
		}
	}
	if len(older) > 0 {
		sort.Slice(older, func(i, j int) bool { return older[i].created.Before(older[j].created) })
		o := older[0]
		return []IngressProblem{{Level: "error",
			Text: "the older ingress " + o.name + " in " + o.namespace + " answers this address",
			Fix:  &IngressFix{Kind: "ingress", Namespace: o.namespace, Name: o.name}}}
	}
	if len(newer) > 0 {
		n := newer[0]
		return []IngressProblem{{Level: "warn",
			Text: "also claimed by " + n.name + " in " + n.namespace + ", which is newer and ignored",
			Fix:  &IngressFix{Kind: "ingress", Namespace: n.namespace, Name: n.name}}}
	}
	return nil
}

func (w *ingressWorld) annotationKeys() []string {
	seen := map[string]bool{}
	for _, ing := range w.all {
		for k := range ing.Annotations {
			if !hiddenAnnotation(k) {
				seen[k] = true
			}
		}
	}
	return sortedSet(seen)
}

func (w *ingressWorld) classList() []string {
	set := map[string]bool{}
	for c := range w.classes {
		set[c] = true
	}
	return sortedSet(set)
}

func sortedSet(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// --- reading ---------------------------------------------------------------

// ListIngresses returns every route the namespace's ingresses answer.
func (m *Manager) ListIngresses(ctx context.Context, asUser, namespace string) (*IngressList, error) {
	w, cs, err := m.world(ctx, asUser, namespace)
	if err != nil {
		return nil, err
	}
	l, err := cs.NetworkingV1().Ingresses(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	out := &IngressList{Routes: []IngressRoute{}, AnnotationKeys: w.annotationKeys(), Classes: w.classList()}
	for i := range l.Items {
		out.Routes = append(out.Routes, w.routes(&l.Items[i])...)
	}
	sort.SliceStable(out.Routes, func(i, j int) bool {
		a, b := out.Routes[i], out.Routes[j]
		if a.Host != b.Host {
			return a.Host < b.Host
		}
		return a.Path < b.Path
	})
	return out, nil
}

// GetIngress opens one ingress for editing.
func (m *Manager) GetIngress(ctx context.Context, asUser, namespace, name string) (*IngressDoc, error) {
	w, cs, err := m.world(ctx, asUser, namespace)
	if err != nil {
		return nil, err
	}
	ing, err := cs.NetworkingV1().Ingresses(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	return docOf(w, ing), nil
}

func docOf(w *ingressWorld, ing *networkingv1.Ingress) *IngressDoc {
	d := &IngressDoc{Name: ing.Name, Version: ing.ResourceVersion,
		Release: ing.Annotations["meta.helm.sh/release-name"], IngressForm: formOf(ing), YAML: renderIngress(ing)}
	if ing.Spec.DefaultBackend != nil && ing.Spec.DefaultBackend.Service != nil {
		d.DefaultBackend = ing.Spec.DefaultBackend.Service.Name + ":" + portOf(ing.Spec.DefaultBackend.Service)
	}
	d.Routes = w.routes(ing)
	if d.Routes == nil {
		d.Routes = []IngressRoute{}
	}
	return d
}

func formOf(ing *networkingv1.Ingress) IngressForm {
	f := IngressForm{Class: classOf(ing), Rules: []IngressRule{}, TLS: []IngressTLS{}, Annotations: map[string]string{}}
	for k, v := range ing.Annotations {
		if !hiddenAnnotation(k) && k != "kubernetes.io/ingress.class" {
			f.Annotations[k] = v
		}
	}
	for _, r := range ing.Spec.Rules {
		if r.HTTP == nil {
			continue
		}
		for _, p := range r.HTTP.Paths {
			rule := IngressRule{Host: r.Host, Path: p.Path}
			if p.PathType != nil {
				rule.PathType = string(*p.PathType)
			}
			if p.Backend.Service != nil {
				rule.Service, rule.Port = p.Backend.Service.Name, portOf(p.Backend.Service)
			}
			f.Rules = append(f.Rules, rule)
		}
	}
	for _, t := range ing.Spec.TLS {
		f.TLS = append(f.TLS, IngressTLS{Secret: t.SecretName, Hosts: append([]string{}, t.Hosts...)})
	}
	return f
}

// editable is the part of an ingress edited as YAML: annotations and spec.
type editable struct {
	Metadata struct {
		Annotations map[string]string `json:"annotations,omitempty"`
	} `json:"metadata"`
	Spec networkingv1.IngressSpec `json:"spec"`
}

func renderIngress(ing *networkingv1.Ingress) string {
	var e editable
	for k, v := range ing.Annotations {
		if !hiddenAnnotation(k) {
			if e.Metadata.Annotations == nil {
				e.Metadata.Annotations = map[string]string{}
			}
			e.Metadata.Annotations[k] = v
		}
	}
	e.Spec = ing.Spec
	b, err := yaml.Marshal(e)
	if err != nil {
		return ""
	}
	return string(b)
}

// --- suggestions for a new one ---------------------------------------------

// IngressSuggestion is a new ingress for a service, filled in from what the
// cluster already does.
type IngressSuggestion struct {
	Name    string   `json:"name"`
	Host    string   `json:"host"`
	Path    string   `json:"path"`
	Port    string   `json:"port"`
	Ports   []string `json:"ports"`
	Class   string   `json:"class"`
	TLS     string   `json:"tls,omitempty"`
	Secrets []string `json:"secrets"` // TLS secrets of the namespace, best first
}

// SuggestIngress proposes an ingress for a service.
func (m *Manager) SuggestIngress(ctx context.Context, asUser, namespace, service string) (*IngressSuggestion, error) {
	w, _, err := m.world(ctx, asUser, namespace)
	if err != nil {
		return nil, err
	}
	svc, ok := w.services[service]
	if !ok {
		return nil, fmt.Errorf("no service %s in %s", service, namespace)
	}
	return w.suggest(svc, namespace), nil
}

func (w *ingressWorld) suggest(svc corev1.Service, namespace string) *IngressSuggestion {
	s := &IngressSuggestion{Name: svc.Name, Path: "/", Port: likelyPort(svc), Class: w.likelyClass(namespace)}
	for _, p := range svc.Spec.Ports {
		s.Ports = append(s.Ports, strconv.Itoa(int(p.Port)))
	}
	if s.Port == "" && len(s.Ports) > 0 {
		s.Port = s.Ports[0]
	}
	if d := w.likelyDomain(namespace); d != "" {
		s.Host = svc.Name + "." + d
		s.TLS, _ = w.bestCert(s.Host, "")
	}
	for n, c := range w.certs {
		if c != nil {
			s.Secrets = append(s.Secrets, n)
		}
	}
	sort.Slice(s.Secrets, func(i, j int) bool {
		if s.Secrets[i] == s.TLS {
			return true
		}
		if s.Secrets[j] == s.TLS {
			return false
		}
		return s.Secrets[i] < s.Secrets[j]
	})
	return s
}

// likelyDomain is the domain most hosts use: the namespace's own if it has
// any, the cluster's otherwise.
func (w *ingressWorld) likelyDomain(namespace string) string {
	count := func(own bool) string {
		n := map[string]int{}
		for _, ing := range w.all {
			if own != (ing.Namespace == namespace) {
				continue
			}
			for _, r := range ing.Spec.Rules {
				if _, d, ok := strings.Cut(r.Host, "."); ok && strings.Contains(d, ".") {
					n[d]++
				}
			}
		}
		best, most := "", 0
		for d, c := range n {
			if c > most || (c == most && d < best) {
				best, most = d, c
			}
		}
		return best
	}
	if d := count(true); d != "" {
		return d
	}
	return count(false)
}

// likelyClass is the class marked default, or the one the namespace's
// neighbours use most.
func (w *ingressWorld) likelyClass(namespace string) string {
	if w.defaultClass != "" {
		return w.defaultClass
	}
	n := map[string]int{}
	for _, ing := range w.all {
		if c := classOf(&ing); c != "" {
			if ing.Namespace == namespace {
				n[c] += 100
			}
			n[c]++
		}
	}
	best, most := "", 0
	for c, k := range n {
		if k > most || (k == most && c < best) {
			best, most = c, k
		}
	}
	return best
}

// CheckIngress says what would stand in the way of an ingress before it is
// saved: the same checks the list makes, against the cluster as it is.
func (m *Manager) CheckIngress(ctx context.Context, asUser, namespace, name string, f IngressForm) ([]IngressRoute, error) {
	w, cs, err := m.world(ctx, asUser, namespace)
	if err != nil {
		return nil, err
	}
	ing := &networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}}
	if cur, err := cs.NetworkingV1().Ingresses(namespace).Get(ctx, name, metav1.GetOptions{}); err == nil {
		ing = cur
	}
	if err := applyForm(ing, f); err != nil {
		return nil, err
	}
	// Not created yet, or about to change: neither has an address to wait for.
	ing.Status = networkingv1.IngressStatus{}
	ing.CreationTimestamp = metav1.NewTime(w.now)
	return w.routes(ing), nil
}

// --- writing ---------------------------------------------------------------

func pathTypeOf(s string) *networkingv1.PathType {
	t := networkingv1.PathTypePrefix
	switch networkingv1.PathType(s) {
	case networkingv1.PathTypeExact, networkingv1.PathTypeImplementationSpecific:
		t = networkingv1.PathType(s)
	}
	return &t
}

func backendOf(service, port string) (*networkingv1.IngressServiceBackend, error) {
	if service == "" || port == "" {
		return nil, errors.New("every path needs a service and a port")
	}
	b := &networkingv1.IngressServiceBackend{Name: service}
	if n, err := strconv.Atoi(port); err == nil {
		b.Port.Number = int32(n)
	} else {
		b.Port.Name = port
	}
	return b, nil
}

// applyForm puts the form's rules, certificates, class and annotations on an
// ingress. A default backend, labels and the annotations its installer keeps
// are left as they were.
func applyForm(ing *networkingv1.Ingress, f IngressForm) error {
	var rules []networkingv1.IngressRule
	index := map[string]int{}
	for _, r := range f.Rules {
		host := strings.TrimSpace(r.Host)
		path := strings.TrimSpace(r.Path)
		if path == "" {
			path = "/"
		}
		if !strings.HasPrefix(path, "/") {
			return fmt.Errorf("the path %q has to start with /", path)
		}
		be, err := backendOf(strings.TrimSpace(r.Service), strings.TrimSpace(r.Port))
		if err != nil {
			return err
		}
		i, ok := index[host]
		if !ok {
			i = len(rules)
			index[host] = i
			rules = append(rules, networkingv1.IngressRule{Host: host,
				IngressRuleValue: networkingv1.IngressRuleValue{HTTP: &networkingv1.HTTPIngressRuleValue{}}})
		}
		rules[i].HTTP.Paths = append(rules[i].HTTP.Paths, networkingv1.HTTPIngressPath{
			Path: path, PathType: pathTypeOf(r.PathType), Backend: networkingv1.IngressBackend{Service: be}})
	}
	if len(rules) == 0 && ing.Spec.DefaultBackend == nil {
		return errors.New("an ingress needs at least one path")
	}
	ing.Spec.Rules = rules

	ing.Spec.TLS = nil
	for _, t := range f.TLS {
		if strings.TrimSpace(t.Secret) == "" {
			continue
		}
		ing.Spec.TLS = append(ing.Spec.TLS, networkingv1.IngressTLS{SecretName: strings.TrimSpace(t.Secret), Hosts: t.Hosts})
	}

	if f.Class != "" {
		c := f.Class
		ing.Spec.IngressClassName = &c
	} else {
		ing.Spec.IngressClassName = nil
	}

	kept := map[string]string{}
	for k, v := range ing.Annotations {
		if hiddenAnnotation(k) {
			kept[k] = v
		}
	}
	for k, v := range f.Annotations {
		k = strings.TrimSpace(k)
		if k == "" || hiddenAnnotation(k) {
			continue
		}
		kept[k] = v
	}
	if ing.Spec.IngressClassName != nil {
		delete(kept, "kubernetes.io/ingress.class") // superseded by the field
	}
	ing.Annotations = kept
	return nil
}

// applyYAML puts edited YAML — annotations and spec — on an ingress.
func applyYAML(ing *networkingv1.Ingress, text string) error {
	var e editable
	if err := yaml.UnmarshalStrict([]byte(text), &e); err != nil {
		return fmt.Errorf("the YAML does not read as annotations and spec: %w", err)
	}
	f := IngressForm{Annotations: e.Metadata.Annotations}
	ing.Spec.DefaultBackend = e.Spec.DefaultBackend
	tmp := ing.DeepCopy()
	tmp.Spec = e.Spec
	g := formOf(tmp)
	f.Rules, f.TLS, f.Class = g.Rules, g.TLS, g.Class
	if f.Annotations == nil {
		f.Annotations = map[string]string{}
	}
	return applyForm(ing, f)
}

// IngressChange is a create or an update: either the form or YAML.
type IngressChange struct {
	Form *IngressForm
	YAML *string
}

// CreateIngress creates an ingress from the form, and describes it.
func (m *Manager) CreateIngress(ctx context.Context, asUser, namespace, name string, ch IngressChange) (string, error) {
	cs, err := m.Clientset(asUser)
	if err != nil {
		return "", err
	}
	ing := &networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}}
	if err := applyChange(ing, ch); err != nil {
		return "", err
	}
	if _, err := cs.NetworkingV1().Ingresses(namespace).Create(ctx, ing, metav1.CreateOptions{}); err != nil {
		return "", err
	}
	return describeIngress(nil, ing), nil
}

func applyChange(ing *networkingv1.Ingress, ch IngressChange) error {
	switch {
	case ch.YAML != nil:
		return applyYAML(ing, *ch.YAML)
	case ch.Form != nil:
		return applyForm(ing, *ch.Form)
	}
	return errors.New("nothing to save")
}

// UpdateIngress saves a change, provided nobody else has since version.
func (m *Manager) UpdateIngress(ctx context.Context, asUser, namespace, name, version string, ch IngressChange) (*IngressDoc, string, error) {
	w, cs, err := m.world(ctx, asUser, namespace)
	if err != nil {
		return nil, "", err
	}
	return updateIngress(ctx, cs, w, namespace, name, version, ch)
}

func updateIngress(ctx context.Context, cs kubernetes.Interface, w *ingressWorld, namespace, name, version string,
	ch IngressChange) (*IngressDoc, string, error) {
	ings := cs.NetworkingV1().Ingresses(namespace)
	cur, err := ings.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, "", err
	}
	if cur.ResourceVersion != version {
		return nil, "", ErrIngressChanged
	}
	before := cur.DeepCopy()
	if err := applyChange(cur, ch); err != nil {
		return nil, "", err
	}
	saved, err := ings.Update(ctx, cur, metav1.UpdateOptions{})
	if apierrors.IsConflict(err) {
		return nil, "", ErrIngressChanged
	}
	if err != nil {
		return nil, "", err
	}
	return docOf(w, saved), describeIngress(before, saved), nil
}

// DeleteIngress removes an ingress and says which addresses went with it.
func (m *Manager) DeleteIngress(ctx context.Context, asUser, namespace, name string) (string, error) {
	cs, err := m.Clientset(asUser)
	if err != nil {
		return "", err
	}
	var detail string
	if ing, err := cs.NetworkingV1().Ingresses(namespace).Get(ctx, name, metav1.GetOptions{}); err == nil {
		detail = describeIngress(ing, nil)
	}
	return detail, cs.NetworkingV1().Ingresses(namespace).Delete(ctx, name, metav1.DeleteOptions{})
}

// --- describing a change ---------------------------------------------------

func routeSet(ing *networkingv1.Ingress) map[string]string {
	out := map[string]string{}
	if ing == nil {
		return out
	}
	for _, r := range ing.Spec.Rules {
		if r.HTTP == nil {
			continue
		}
		for _, p := range r.HTTP.Paths {
			to := ""
			if p.Backend.Service != nil {
				to = p.Backend.Service.Name + ":" + portOf(p.Backend.Service)
			}
			path := p.Path
			if path == "" {
				path = "/"
			}
			out[r.Host+path] = to
		}
	}
	return out
}

// describeIngress says what changed between two states of an ingress: routes
// added, removed and redirected, certificates, class, and which settings —
// their names, as everywhere in the trail.
func describeIngress(before, after *networkingv1.Ingress) string {
	b, a := routeSet(before), routeSet(after)
	var parts []string
	for _, k := range sortedKeysOf(a) {
		switch old, ok := b[k]; {
		case !ok:
			parts = append(parts, "added "+k+" → "+a[k])
		case old != a[k]:
			parts = append(parts, k+" → "+a[k]+" (was "+old+")")
		}
	}
	for _, k := range sortedKeysOf(b) {
		if _, ok := a[k]; !ok {
			parts = append(parts, "removed "+k)
		}
	}
	tls := func(ing *networkingv1.Ingress) string {
		if ing == nil {
			return ""
		}
		var s []string
		for _, t := range ing.Spec.TLS {
			s = append(s, t.SecretName)
		}
		sort.Strings(s)
		return strings.Join(s, ", ")
	}
	if before != nil && after != nil && tls(before) != tls(after) {
		parts = append(parts, "TLS "+orNone(tls(after))+" (was "+orNone(tls(before))+")")
	}
	if before != nil && after != nil && classOf(before) != classOf(after) {
		parts = append(parts, "class "+orNone(classOf(after)))
	}
	if before != nil && after != nil {
		var changed []string
		keys := map[string]bool{}
		for k := range before.Annotations {
			keys[k] = true
		}
		for k := range after.Annotations {
			keys[k] = true
		}
		for _, k := range sortedSet(keys) {
			if hiddenAnnotation(k) {
				continue
			}
			if before.Annotations[k] != after.Annotations[k] {
				changed = append(changed, k)
			}
		}
		if len(changed) > 0 {
			parts = append(parts, "settings changed: "+strings.Join(changed, ", "))
		}
	}
	if len(parts) == 0 {
		return "no change"
	}
	return strings.Join(parts, "; ")
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

func sortedKeysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
