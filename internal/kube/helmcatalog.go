package kube

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
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
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/yaml"
)

// Charts from the catalog are installed with the portal's own account, not
// the requester's: a developer holds no right to create a StatefulSet, and the
// point is that they need not. What keeps that from being a way round every
// other rule is the plan below and the administrator who reads it: the chart
// is rendered first, and anything that reaches past the namespace, or into the
// node, is spelled out as a warning before anyone is asked to approve it. What
// is acceptable is the administrator's call, not the portal's.

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

// PlanProblem is something a plan found. Errors are what cannot work at all —
// the chart does not render, the name is taken; warnings are for the people
// deciding.
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
	// Notes are what the chart says comes after the install — its
	// NOTES.txt, rendered with these values.
	Notes string `json:"notes,omitempty"`
	// Required are values the chart refused to render without, read from
	// its error, so they can be asked for one by one.
	Required []RequiredValue `json:"required,omitempty"`
	// NewNamespace: the namespace does not exist yet; it would be created.
	NewNamespace bool `json:"newNamespace,omitempty"`
	// Refusal is what the chart said when it would not render, in its
	// own words, one point each.
	Refusal *Refusal `json:"refusal,omitempty"`

	// Timings are how long each part of the plan took, for the log.
	Timings []Timing `json:"-"`

	// unknownKind: an object of a kind the cluster was not known to have.
	unknownKind bool
}

// Timing is one part of a plan and how long it took.
type Timing struct {
	Part string
	Took time.Duration
}

// Refusal is a chart's own reasons for not rendering, and where it said so.
type Refusal struct {
	Where    string   `json:"where,omitempty"`
	Messages []string `json:"messages"`
}

// RequiredValue is a key a chart asked for, with what it said.
type RequiredValue struct {
	Key  string `json:"key"`
	Said string `json:"said"`
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

	began := time.Now()
	defined := chartKinds(ch)
	if len(defined) > 0 {
		names := make([]string, 0, len(defined))
		for gk := range defined {
			names = append(names, gk.Kind)
		}
		sort.Strings(names)
		for _, n := range names {
			plan.Objects = append(plan.Objects, PlanObject{Kind: "CustomResourceDefinition", Name: n})
		}
		if upgrade {
			plan.warn("the chart brings resource definitions (%s) — cluster-wide; an upgrade does not change them or add new ones",
				strings.Join(names, ", "))
		} else {
			plan.warn("the chart brings resource definitions (%s) — cluster-wide, shared by every namespace, and left behind by an uninstall",
				strings.Join(names, ", "))
		}
	}

	exists, err := m.NamespaceExists(ctx, namespace)
	if err != nil {
		return nil, err
	}
	if !exists {
		if upgrade {
			plan.fail("there is no namespace %s to upgrade in", namespace)
			return plan, nil
		}
		// Nothing to ask the cluster about a namespace that is not there:
		// rendered as helm template would, it is created with the release.
		plan.NewNamespace = true
		plan.Timings = append(plan.Timings, Timing{"prepare", time.Since(began)})
		began = time.Now()
		rel, err := m.renderOffline(namespace, name, ch, vals)
		plan.Timings = append(plan.Timings, Timing{"render", time.Since(began)})
		if err != nil {
			plan.notRendered(err, ch)
			m.askAllAtOnce(ctx, plan, namespace, name, ch, vals)
			return plan, nil
		}
		return m.finishPlan(plan, rel, defined, namespace)
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

	plan.Timings = append(plan.Timings, Timing{"prepare", time.Since(began)})
	began = time.Now()
	rel, err := m.dryRun(ctx, cfg, namespace, name, ch, vals, upgrade)
	plan.Timings = append(plan.Timings, Timing{"render", time.Since(began)})
	if err != nil && staleKinds(err) {
		// A kind the kept list of the cluster's kinds does not have yet.
		m.forgetDiscovery()
		if cfg, _, err = m.helmActionConfig("", namespace); err != nil {
			return nil, err
		}
		rel, err = m.dryRun(ctx, cfg, namespace, name, ch, vals, upgrade)
	}
	if err != nil && len(defined) > 0 {
		// The cluster cannot check objects of kinds the chart has not
		// defined yet; render it without the cluster instead.
		rel, err = m.renderOffline(namespace, name, ch, vals)
	}
	if err != nil {
		plan.notRendered(err, ch)
		m.askAllAtOnce(ctx, plan, namespace, name, ch, vals)
		return plan, nil
	}
	return m.finishPlan(plan, rel, defined, namespace)
}

// staleKinds says whether a render failed on a kind the cluster was not
// known to have.
func staleKinds(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "no matches for kind") || strings.Contains(msg, "resource mapping not found")
}

// dryRun renders a release against the cluster without installing it.
func (m *Manager) dryRun(ctx context.Context, cfg *action.Configuration, namespace, name string, ch *chart.Chart,
	vals map[string]any, upgrade bool) (*release.Release, error) {
	if upgrade {
		u := action.NewUpgrade(cfg)
		u.Namespace = namespace
		u.DryRun, u.DryRunOption = true, "server"
		// The cluster's schema is read when the release is installed; for
		// a plan, the kinds below are enough, and much quicker.
		u.DisableOpenAPIValidation = true
		u.ResetValues = true
		return u.RunWithContext(ctx, name, ch, vals)
	}
	i := action.NewInstall(cfg)
	i.Namespace, i.ReleaseName = namespace, name
	i.DryRun, i.DryRunOption = true, "server"
	i.DisableOpenAPIValidation = true
	return i.RunWithContext(ctx, ch, vals)
}

// probeRounds bounds how many keys in a row a chart is asked about, and
// probeBudget how long that may take.
const (
	probeRounds = 8
	probeBudget = 6 * time.Second
)

// askAllAtOnce finds every key a chart will ask for, not only the first: a
// chart stops at the first value it lacks, so each one found is given a
// stand-in and the chart rendered again, without the cluster, until it asks
// for nothing new. Whoever installs then fills them in one go instead of one
// check at a time.
func (m *Manager) askAllAtOnce(ctx context.Context, plan *ReleasePlan, namespace, name string, ch *chart.Chart, vals map[string]any) {
	if len(plan.Required) == 0 || allKnown(ctx, plan.Required) {
		return
	}
	// A large chart takes seconds to render: the search stops when its time
	// is up, or when whoever asked has moved on to another check.
	start := time.Now()
	defer func() {
		plan.Timings = append(plan.Timings, Timing{"probe", time.Since(start)})
	}()
	probe, err := copyValues(vals)
	if err != nil {
		return
	}
	seen := map[string]bool{}
	said := map[string]bool{}
	for _, msg := range plan.Refusal.Messages {
		said[msg] = true
	}
	fresh := plan.Required
	for round := 0; round < probeRounds && len(fresh) > 0 && ctx.Err() == nil && time.Since(start) < probeBudget; round++ {
		for _, r := range fresh {
			seen[r.Key] = true
			setValuePath(probe, r.Key, "x")
		}
		_, err := m.renderOffline(namespace, name, ch, probe)
		if err == nil {
			return
		}
		fresh = nil
		for _, r := range requiredValues(err.Error(), ch.Metadata.Name) {
			if !seen[r.Key] {
				fresh = append(fresh, r)
				plan.Required = append(plan.Required, r)
			}
		}
		for _, msg := range parseRefusal(err.Error()).Messages {
			if !said[msg] && len(fresh) > 0 {
				said[msg] = true
				plan.Refusal.Messages = append(plan.Refusal.Messages, msg)
			}
		}
	}
}

type knownKey struct{}

// WithKnownRequired says which required values whoever checks already has a
// field for: when the chart asks for none other, there is nothing to search.
func WithKnownRequired(ctx context.Context, keys []string) context.Context {
	set := map[string]bool{}
	for _, k := range keys {
		set[k] = true
	}
	return context.WithValue(ctx, knownKey{}, set)
}

func allKnown(ctx context.Context, required []RequiredValue) bool {
	known, _ := ctx.Value(knownKey{}).(map[string]bool)
	if len(known) == 0 {
		return false
	}
	for _, r := range required {
		if !known[r.Key] {
			return false
		}
	}
	return true
}

// copyValues copies values deeply, so a probe does not touch the real ones.
func copyValues(vals map[string]any) (map[string]any, error) {
	raw, err := yaml.Marshal(vals)
	if err != nil {
		return nil, err
	}
	out := map[string]any{}
	if err := yaml.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// setValuePath sets a.b.c in values, making the maps on the way and
// replacing whatever is not one.
func setValuePath(m map[string]any, path string, v any) {
	parts := strings.Split(path, ".")
	for _, p := range parts[:len(parts)-1] {
		next, ok := m[p].(map[string]any)
		if !ok {
			next = map[string]any{}
			m[p] = next
		}
		m = next
	}
	m[parts[len(parts)-1]] = v
}

// notRendered records why a chart did not render, and the values it named
// as missing.
func (p *ReleasePlan) notRendered(err error, ch *chart.Chart) {
	p.Refusal = parseRefusal(err.Error())
	p.fail("the chart refused these values: %s", shorten(strings.Join(p.Refusal.Messages, " · "), 600))
	p.Required = requiredValues(err.Error(), ch.Metadata.Name)
}

// finishPlan checks every object a rendered release would create.
func (m *Manager) finishPlan(plan *ReleasePlan, rel *release.Release, defined map[schema.GroupKind]bool, namespace string) (*ReleasePlan, error) {
	if rel.Info != nil {
		plan.Notes = shorten(strings.TrimSpace(rel.Info.Notes), 4000)
	}
	// Checked against the kept list of the cluster's kinds; a kind it lacks
	// sends the check round once more, with a fresh list.
	began := time.Now()
	defer func() { plan.Timings = append(plan.Timings, Timing{"objects", time.Since(began)}) }()
	base := *plan
	base.Objects = append([]PlanObject{}, plan.Objects...)
	base.Problems = append([]PlanProblem{}, plan.Problems...)
	base.Images = append([]string{}, plan.Images...)
	for attempt := 0; ; attempt++ {
		mapper, err := m.portalGetter(namespace).ToRESTMapper()
		if err != nil {
			return nil, fmt.Errorf("read the cluster's kinds: %w", err)
		}
		m.checkManifests(plan, rel, mapper, defined, namespace)
		if !plan.unknownKind || attempt > 0 {
			return plan, nil
		}
		m.forgetDiscovery()
		*plan = base
		plan.Objects = append([]PlanObject{}, base.Objects...)
		plan.Problems = append([]PlanProblem{}, base.Problems...)
		plan.Images = append([]string{}, base.Images...)
	}
}

// checkManifests checks each object of a rendered release.
func (m *Manager) checkManifests(plan *ReleasePlan, rel *release.Release, mapper meta.RESTMapper, defined map[schema.GroupKind]bool, namespace string) {
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
		checkObject(plan, mapper, defined, namespace, &unstructured.Unstructured{Object: obj}, mf.hook)
	}
	sort.SliceStable(plan.Objects, func(i, j int) bool { return plan.Objects[i].Kind < plan.Objects[j].Kind })
	plan.Images = uniqStrings(plan.Images)
}

// renderOffline renders a chart the way helm template does, without asking the
// cluster — for a chart whose objects are of kinds it brings itself.
func (m *Manager) renderOffline(namespace, name string, ch *chart.Chart, vals map[string]any) (*release.Release, error) {
	cfg, _, err := m.helmActionConfig("", namespace)
	if err != nil {
		return nil, err
	}
	i := action.NewInstall(cfg)
	i.Namespace, i.ReleaseName = namespace, name
	i.DryRun, i.DryRunOption, i.ClientOnly = true, "client", true
	return i.Run(ch, vals)
}

// chartKinds are the kinds a chart defines in its crds/ directories, each
// with whether it is cluster-wide.
func chartKinds(ch *chart.Chart) map[schema.GroupKind]bool {
	// Read once per chart version: resource definitions run to megabytes,
	// and a chart is checked at every change of its values.
	key := ch.Metadata.Name + "@" + ch.Metadata.Version
	kindsMu.Lock()
	got, ok := kindsSeen[key]
	kindsMu.Unlock()
	if ok {
		return got
	}
	out := readChartKinds(ch)
	kindsMu.Lock()
	if len(kindsSeen) > 64 {
		kindsSeen = map[string]map[schema.GroupKind]bool{}
	}
	kindsSeen[key] = out
	kindsMu.Unlock()
	return out
}

var (
	kindsMu   sync.Mutex
	kindsSeen = map[string]map[schema.GroupKind]bool{}
)

func readChartKinds(ch *chart.Chart) map[schema.GroupKind]bool {
	out := map[schema.GroupKind]bool{}
	for _, crd := range ch.CRDObjects() {
		for _, text := range releaseutil.SplitManifests(string(crd.File.Data)) {
			var d struct {
				Kind string `json:"kind"`
				Spec struct {
					Group string `json:"group"`
					Scope string `json:"scope"`
					Names struct {
						Kind string `json:"kind"`
					} `json:"names"`
				} `json:"spec"`
			}
			if yaml.Unmarshal([]byte(text), &d) != nil || d.Kind != "CustomResourceDefinition" || d.Spec.Names.Kind == "" {
				continue
			}
			out[schema.GroupKind{Group: d.Spec.Group, Kind: d.Spec.Names.Kind}] = d.Spec.Scope == "Cluster"
		}
	}
	return out
}

// checkObject adds one rendered object to the plan, and a warning for
// whatever reaches past the namespace. defined are the kinds the chart brings
// itself, each with whether it is cluster-wide.
func checkObject(plan *ReleasePlan, mapper meta.RESTMapper, defined map[schema.GroupKind]bool, namespace string, u *unstructured.Unstructured, hook bool) {
	kind, name := u.GetKind(), u.GetName()
	what := kind + " " + name
	plan.Objects = append(plan.Objects, PlanObject{Kind: kind, Name: name, Hook: hook})

	gvk := u.GroupVersionKind()
	clusterWide, ok := defined[gvk.GroupKind()]
	if !ok {
		mapping, err := mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
		if err != nil {
			plan.unknownKind = true
			plan.fail("%s: the cluster has no kind %s (%s), and the chart does not define it", what, kind, u.GetAPIVersion())
			return
		}
		clusterWide = mapping.Scope.Name() == meta.RESTScopeNameRoot
	}
	if clusterWide {
		plan.warn("%s is cluster-wide — not inside %s, it affects the whole cluster", what, namespace)
	} else if ns := u.GetNamespace(); ns != "" && ns != namespace {
		plan.warn("%s goes into another namespace, %s", what, ns)
	}

	switch kind {
	case "RoleBinding":
		ref, _, _ := unstructured.NestedString(u.Object, "roleRef", "name")
		refKind, _, _ := unstructured.NestedString(u.Object, "roleRef", "kind")
		if refKind == "ClusterRole" && (ref == "cluster-admin" || ref == "admin" || ref == "edit") {
			plan.warn("%s grants the cluster role %s", what, ref)
		}
	case "ClusterRoleBinding":
		if ref, _, _ := unstructured.NestedString(u.Object, "roleRef", "name"); ref == "cluster-admin" {
			plan.warn("%s makes its subjects cluster administrators", what)
		}
	case "Service":
		t, _, _ := unstructured.NestedString(u.Object, "spec", "type")
		if t == "NodePort" || t == "LoadBalancer" {
			plan.warn("%s is a %s — reachable from outside the cluster", what, t)
		}
	}

	if spec := podSpecOf(u); spec != nil {
		if why := nodeAccess(spec); why != "" {
			plan.warn("%s: %s", what, why)
		}
		for _, c := range spec.InitContainers {
			if sc := c.SecurityContext; sc != nil && sc.Privileged != nil && *sc.Privileged {
				plan.warn("%s: init container %s is privileged", what, c.Name)
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
	// Secrets are the namespace's other Secrets, names and keys only, for a
	// chart that takes the name of one holding a password.
	Secrets []SecretHint `json:"secrets"`
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
	h := &ClusterHints{StorageClasses: []StorageClassHint{}, IngressClasses: []string{}, Certs: []CertHint{}, Secrets: []SecretHint{}}
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
	if secrets, err := cs.CoreV1().Secrets(namespace).List(ctx, metav1.ListOptions{}); err == nil {
		h.Secrets = settingSecrets(secrets.Items)
	}
	return h
}
