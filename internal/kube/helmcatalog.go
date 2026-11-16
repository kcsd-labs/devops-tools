package kube

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/release"
	"helm.sh/helm/v3/pkg/releaseutil"
	"helm.sh/helm/v3/pkg/storage/driver"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/yaml"
)

// Charts from the catalog are installed with the portal's own account, not
// the requester's: a developer holds no right to create a StatefulSet, and the
// point is that they need not. What keeps that from being a way round every
// other rule is the plan below — the chart is rendered first, and anything
// that reaches past the namespace, or into the node, is refused before anyone
// is asked to approve it.

// Labels on a release installed from the catalog, saying where it came from.
// Helm keeps them on the release, so they survive the portal and are how an
// upgrade knows which chart to offer.
const (
	labelCatalogRepo  = "catalog-repo"
	labelCatalogChart = "catalog-chart"
)

// CatalogOrigin is where a release from the catalog came from.
type CatalogOrigin struct {
	Repo  string `json:"repo"`
	Chart string `json:"chart"`
}

func catalogOrigin(r *release.Release) *CatalogOrigin {
	if r.Labels[labelCatalogRepo] == "" || r.Labels[labelCatalogChart] == "" {
		return nil
	}
	return &CatalogOrigin{Repo: r.Labels[labelCatalogRepo], Chart: r.Labels[labelCatalogChart]}
}

// CatalogLabels are the labels a catalog install puts on its release.
func CatalogLabels(repo, chartName string) map[string]string {
	if !labelValue(repo) || !labelValue(chartName) {
		return nil
	}
	return map[string]string{labelCatalogRepo: repo, labelCatalogChart: chartName}
}

func labelValue(s string) bool {
	if s == "" || len(s) > 63 {
		return false
	}
	for i, c := range s {
		ok := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
			(i > 0 && i < len(s)-1 && (c == '-' || c == '_' || c == '.'))
		if !ok {
			return false
		}
	}
	return true
}

// PlanProblem is something a plan found. Errors stop the install; warnings are
// for the people deciding.
type PlanProblem struct {
	Level string `json:"level"` // error | warn
	Text  string `json:"text"`
}

// PlanObject is one object the release would create.
type PlanObject struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
	Hook bool   `json:"hook,omitempty"`
}

// ReleasePlan is what installing a chart with some values would do.
type ReleasePlan struct {
	Objects  []PlanObject  `json:"objects"`
	Problems []PlanProblem `json:"problems"`
	// Images are what the pods would run, for the registry check.
	Images []string `json:"images"`
}

// OK reports whether nothing in the plan stops the install.
func (p *ReleasePlan) OK() bool {
	for _, pr := range p.Problems {
		if pr.Level == "error" {
			return false
		}
	}
	return true
}

func (p *ReleasePlan) fail(format string, a ...any) {
	p.Problems = append(p.Problems, PlanProblem{Level: "error", Text: fmt.Sprintf(format, a...)})
}

func (p *ReleasePlan) warn(format string, a ...any) {
	p.Problems = append(p.Problems, PlanProblem{Level: "warn", Text: fmt.Sprintf(format, a...)})
}

// PlanRelease renders the chart against the cluster without installing it,
// and checks what it would create. upgrade plans an upgrade of an existing
// release instead of a new one.
func (m *Manager) PlanRelease(ctx context.Context, namespace, name string, ch *chart.Chart, vals map[string]any, upgrade bool) (*ReleasePlan, error) {
	plan := &ReleasePlan{Objects: []PlanObject{}, Problems: []PlanProblem{}, Images: []string{}}

	if crds := ch.CRDObjects(); len(crds) > 0 {
		plan.fail("the chart brings its own resource definitions (%s) — they are cluster-wide, so it is not installed from the catalog",
			strings.TrimSuffix(crds[0].Filename, ".yaml"))
		return plan, nil
	}

	cfg, _, err := m.helmActionConfig("", namespace)
	if err != nil {
		return nil, err
	}
	existing, err := action.NewHistory(cfg).Run(name)
	switch {
	case errors.Is(err, driver.ErrReleaseNotFound):
		if upgrade {
			plan.fail("there is no release %s in %s to upgrade", name, namespace)
			return plan, nil
		}
	case err != nil:
		return nil, fmt.Errorf("look for release %s: %w", name, err)
	case !upgrade && len(existing) > 0:
		plan.fail("a release named %s is already in %s — choose another name", name, namespace)
		return plan, nil
	}

	var rel *release.Release
	if upgrade {
		u := action.NewUpgrade(cfg)
		u.Namespace = namespace
		u.DryRun, u.DryRunOption = true, "server"
		u.ResetValues = true
		rel, err = u.RunWithContext(ctx, name, ch, vals)
	} else {
		i := action.NewInstall(cfg)
		i.Namespace, i.ReleaseName = namespace, name
		i.DryRun, i.DryRunOption = true, "server"
		rel, err = i.RunWithContext(ctx, ch, vals)
	}
	if err != nil {
		plan.fail("the chart does not render with these values: %s", trimHelmError(err))
		return plan, nil
	}

	mapper, err := newRESTClientGetter(m.restConfig(""), namespace).ToRESTMapper()
	if err != nil {
		return nil, fmt.Errorf("read the cluster's kinds: %w", err)
	}
	manifests := []struct {
		text string
		hook bool
	}{}
	for _, text := range releaseutil.SplitManifests(rel.Manifest) {
		manifests = append(manifests, struct {
			text string
			hook bool
		}{text, false})
	}
	for _, h := range rel.Hooks {
		manifests = append(manifests, struct {
			text string
			hook bool
		}{h.Manifest, true})
	}
	for _, mf := range manifests {
		var obj map[string]any
		if err := yaml.Unmarshal([]byte(mf.text), &obj); err != nil {
			plan.fail("the chart renders something that is not YAML: %v", err)
			continue
		}
		if len(obj) == 0 {
			continue
		}
		checkObject(plan, mapper, namespace, &unstructured.Unstructured{Object: obj}, mf.hook)
	}
	sort.SliceStable(plan.Objects, func(i, j int) bool { return plan.Objects[i].Kind < plan.Objects[j].Kind })
	plan.Images = uniqStrings(plan.Images)
	return plan, nil
}

// checkObject adds one rendered object to the plan, and whatever is wrong
// with it.
func checkObject(plan *ReleasePlan, mapper meta.RESTMapper, namespace string, u *unstructured.Unstructured, hook bool) {
	kind, name := u.GetKind(), u.GetName()
	what := kind + " " + name
	plan.Objects = append(plan.Objects, PlanObject{Kind: kind, Name: name, Hook: hook})

	gvk := u.GroupVersionKind()
	mapping, err := mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	switch {
	case err != nil:
		plan.fail("%s: the cluster has no kind %s (%s)", what, kind, u.GetAPIVersion())
		return
	case mapping.Scope.Name() == meta.RESTScopeNameRoot:
		plan.fail("%s is cluster-wide — the catalog installs only into this namespace", what)
		return
	}
	if ns := u.GetNamespace(); ns != "" && ns != namespace {
		plan.fail("%s goes into namespace %s — the catalog installs only into %s", what, ns, namespace)
	}

	switch kind {
	case "RoleBinding":
		ref, _, _ := unstructured.NestedString(u.Object, "roleRef", "name")
		refKind, _, _ := unstructured.NestedString(u.Object, "roleRef", "kind")
		if refKind == "ClusterRole" && (ref == "cluster-admin" || ref == "admin" || ref == "edit") {
			plan.fail("%s grants the cluster role %s", what, ref)
		}
	case "Service":
		t, _, _ := unstructured.NestedString(u.Object, "spec", "type")
		if t == "NodePort" || t == "LoadBalancer" {
			plan.warn("%s is a %s — reachable from outside the cluster", what, t)
		}
	}

	if spec := podSpecOf(u); spec != nil {
		if why := nodeAccess(spec); why != "" {
			plan.fail("%s: %s", what, why)
		}
		for _, c := range spec.InitContainers {
			if sc := c.SecurityContext; sc != nil && sc.Privileged != nil && *sc.Privileged {
				plan.fail("%s: init container %s is privileged", what, c.Name)
			}
		}
		for _, c := range append(append([]corev1.Container{}, spec.InitContainers...), spec.Containers...) {
			plan.Images = append(plan.Images, c.Image)
		}
	}
}

// podSpecOf finds the pod template in a workload, or nil when it has none.
func podSpecOf(u *unstructured.Unstructured) *corev1.PodSpec {
	var path []string
	switch u.GetKind() {
	case "Pod":
		path = []string{"spec"}
	case "Deployment", "StatefulSet", "DaemonSet", "ReplicaSet", "Job", "ReplicationController":
		path = []string{"spec", "template", "spec"}
	case "CronJob":
		path = []string{"spec", "jobTemplate", "spec", "template", "spec"}
	default:
		return nil
	}
	m, ok, _ := unstructured.NestedMap(u.Object, path...)
	if !ok {
		return nil
	}
	var spec corev1.PodSpec
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(m, &spec); err != nil {
		return nil
	}
	return &spec
}

// InstallRelease installs a chart and waits for it to come up.
func (m *Manager) InstallRelease(ctx context.Context, namespace, name string, ch *chart.Chart, vals map[string]any, labels map[string]string, timeout time.Duration) error {
	cfg, logs, err := m.helmActionConfig("", namespace)
	if err != nil {
		return err
	}
	i := action.NewInstall(cfg)
	i.Namespace, i.ReleaseName = namespace, name
	i.Wait, i.Timeout = true, timeout
	i.Labels = labels
	_, err = i.RunWithContext(ctx, ch, vals)
	if err != nil {
		return explain(err, logs.trouble())
	}
	return nil
}

// UpgradeRelease upgrades a release to a chart with exactly these values on
// top of the chart's own, and waits for it.
func (m *Manager) UpgradeRelease(ctx context.Context, namespace, name string, ch *chart.Chart, vals map[string]any, labels map[string]string, timeout time.Duration) error {
	cfg, logs, err := m.helmActionConfig("", namespace)
	if err != nil {
		return err
	}
	u := action.NewUpgrade(cfg)
	u.Namespace = namespace
	u.ResetValues = true
	u.Wait, u.Timeout = true, timeout
	u.Labels = labels
	_, err = u.RunWithContext(ctx, name, ch, vals)
	if err != nil {
		return explain(err, logs.trouble())
	}
	return nil
}

// CatalogRelease is a release from the catalog as an upgrade starts from it.
type CatalogRelease struct {
	Origin  CatalogOrigin `json:"origin"`
	Version string        `json:"version"`
	// Values are what was given at the last install or upgrade, not the
	// chart's defaults underneath.
	Values string `json:"values"`
}

// GetCatalogRelease reads a release installed from the catalog. A release
// that was not is ErrNotFromCatalog: its upgrades come from wherever it was
// installed from.
func (m *Manager) GetCatalogRelease(namespace, name string) (*CatalogRelease, error) {
	cfg, _, err := m.helmActionConfig("", namespace)
	if err != nil {
		return nil, err
	}
	rel, err := action.NewGet(cfg).Run(name)
	if err != nil {
		return nil, err
	}
	origin := catalogOrigin(rel)
	if origin == nil {
		return nil, ErrNotFromCatalog
	}
	out := &CatalogRelease{Origin: *origin}
	if rel.Chart != nil && rel.Chart.Metadata != nil {
		out.Version = rel.Chart.Metadata.Version
	}
	if len(rel.Config) > 0 {
		b, err := yaml.Marshal(rel.Config)
		if err != nil {
			return nil, err
		}
		out.Values = string(b)
	}
	return out, nil
}

// ListCatalogReleases lists the releases installed from the catalog, in every
// namespace, read with the portal's account. The caller filters by who may
// see what.
func (m *Manager) ListCatalogReleases() ([]HelmRelease, error) {
	cfg, _, err := m.helmActionConfig("", "")
	if err != nil {
		return nil, err
	}
	list := action.NewList(cfg)
	list.AllNamespaces = true
	list.Selector = labelCatalogRepo
	rels, err := list.Run()
	if err != nil {
		return nil, err
	}
	out := make([]HelmRelease, 0, len(rels))
	for _, r := range rels {
		if catalogOrigin(r) != nil {
			out = append(out, toHelmRelease(r))
		}
	}
	return out, nil
}

// ErrNotFromCatalog says a release was not installed from the catalog.
var ErrNotFromCatalog = errors.New("this release was not installed from the catalog")

// ReleaseTrouble says why a release's pods are not up: the first few that are
// waiting or failing, with the reason Kubernetes gives. Empty when there is
// nothing to say.
func (m *Manager) ReleaseTrouble(ctx context.Context, namespace, name string) string {
	cs, err := m.Clientset("")
	if err != nil {
		return ""
	}
	pods, err := cs.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: "app.kubernetes.io/instance=" + name,
	})
	if err != nil {
		return ""
	}
	var out []string
	for _, p := range pods.Items {
		for _, st := range append(append([]corev1.ContainerStatus{}, p.Status.InitContainerStatuses...), p.Status.ContainerStatuses...) {
			if w := st.State.Waiting; w != nil && w.Reason != "" && w.Reason != "PodInitializing" && w.Reason != "ContainerCreating" {
				msg := "pod " + p.Name + ": " + w.Reason
				if w.Message != "" {
					msg += " — " + shorten(w.Message, 160)
				}
				out = append(out, msg)
				break
			}
		}
		if len(out) == 3 {
			break
		}
	}
	return strings.Join(out, "; ")
}

func trimHelmError(err error) string {
	s := err.Error()
	s = strings.TrimPrefix(s, "INSTALLATION FAILED: ")
	s = strings.TrimPrefix(s, "UPGRADE FAILED: ")
	return shorten(s, 600)
}

func shorten(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func uniqStrings(in []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, s := range in {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

// ClusterHints are what the cluster says about where a chart will run: what
// the install dialog offers instead of making people type it.
type ClusterHints struct {
	StorageClasses []StorageClassHint `json:"storageClasses"`
	IngressClasses []string           `json:"ingressClasses"`
	// IngressClass is the default class, or the one the neighbours use.
	IngressClass string `json:"ingressClass"`
	// Domain is the one most hosts in the namespace — or the cluster — use.
	Domain string `json:"domain"`
	// Certs are the namespace's TLS secrets that have not expired, with the
	// names each covers, so a host can be matched to one.
	Certs []CertHint `json:"certs"`
}

// StorageClassHint is one storage class of the cluster.
type StorageClassHint struct {
	Name    string `json:"name"`
	Default bool   `json:"default,omitempty"`
}

// CertHint is a TLS secret and what it covers.
type CertHint struct {
	Name  string   `json:"name"`
	Names []string `json:"names"`
	Days  int      `json:"days"`
}

// GetClusterHints reads the hints for a namespace with the portal's account.
// Anything it cannot read is left out rather than failing: these are offers.
func (m *Manager) GetClusterHints(ctx context.Context, namespace string) *ClusterHints {
	h := &ClusterHints{StorageClasses: []StorageClassHint{}, IngressClasses: []string{}, Certs: []CertHint{}}
	cs, err := m.Clientset("")
	if err != nil {
		return h
	}
	if scs, err := cs.StorageV1().StorageClasses().List(ctx, metav1.ListOptions{}); err == nil {
		for _, sc := range scs.Items {
			def := sc.Annotations["storageclass.kubernetes.io/is-default-class"] == "true" ||
				sc.Annotations["storageclass.beta.kubernetes.io/is-default-class"] == "true"
			h.StorageClasses = append(h.StorageClasses, StorageClassHint{Name: sc.Name, Default: def})
		}
		sort.Slice(h.StorageClasses, func(i, j int) bool {
			if h.StorageClasses[i].Default != h.StorageClasses[j].Default {
				return h.StorageClasses[i].Default
			}
			return h.StorageClasses[i].Name < h.StorageClasses[j].Name
		})
	}
	w := buildWorld(ctx, cs, cs, namespace)
	for c := range w.classes {
		h.IngressClasses = append(h.IngressClasses, c)
	}
	sort.Strings(h.IngressClasses)
	h.IngressClass = w.likelyClass(namespace)
	h.Domain = w.likelyDomain(namespace)
	for name, c := range w.certs {
		if c == nil || !c.notAfter.After(w.now) {
			continue
		}
		h.Certs = append(h.Certs, CertHint{Name: name, Names: c.names, Days: c.daysLeft(w.now)})
	}
	sort.Slice(h.Certs, func(i, j int) bool { return h.Certs[i].Days > h.Certs[j].Days })
	return h
}
