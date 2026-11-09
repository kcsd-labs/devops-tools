package kube

import (
	"context"
	"errors"
	"path"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/yaml"

	"devops-tools/internal/changes"
)

var (
	ErrConfigMapChanged   = errors.New("someone changed this ConfigMap since you opened it — reload to see their changes, then make yours again")
	ErrConfigMapImmutable = errors.New("this ConfigMap is immutable: Kubernetes refuses any change to it, and it has to be replaced instead")
)

// ConfigMapSummary is a ConfigMap as the list shows it.
type ConfigMapSummary struct {
	Name string `json:"name"`
	Keys int    `json:"keys"`
	// Release is the Helm release that installed it. Its next deploy puts
	// back whatever the chart says, so a change made here does not last.
	Release   string `json:"release,omitempty"`
	Immutable bool   `json:"immutable,omitempty"`
	// System marks the ones nobody edits by hand: the cluster's CA bundles,
	// the portal's own saved requests.
	System bool `json:"system,omitempty"`
	// UsedBy counts the workloads that read it. Nil when they could not be
	// read — unknown is not the same as none.
	UsedBy *int `json:"usedBy,omitempty"`
}

// ConfigMapData is one ConfigMap in full.
type ConfigMapData struct {
	Name string            `json:"name"`
	Data map[string]string `json:"data"`
	// YAML is Data as kubectl shows it — a data: block, multi-line values as
	// literal blocks — for the page's default view. Rendered here so that the
	// page and kubectl agree on every quote.
	YAML string `json:"yaml"`
	// BinaryKeys are shown, not edited: the page edits text.
	BinaryKeys []string `json:"binaryKeys,omitempty"`
	Release    string   `json:"release,omitempty"`
	Immutable  bool     `json:"immutable,omitempty"`
	// Version is sent back with a change, so that two people editing at once
	// do not silently overwrite each other.
	Version string  `json:"version"`
	UsedBy  []Usage `json:"usedBy"`
	// UsageUnknown is set when the workloads could not be read.
	UsageUnknown bool `json:"usageUnknown,omitempty"`
}

// Usage is how one workload reads a ConfigMap — which is what decides whether
// a change reaches it without a restart.
type Usage struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
	// EnvAll means every key, through envFrom. Env lists single keys taken
	// through valueFrom. Either way the values are fixed when the container
	// starts: a change reaches them only with a restart.
	EnvAll bool     `json:"envAll,omitempty"`
	Env    []string `json:"env,omitempty"`
	// Mounts are the places it appears as files. Those are refreshed in the
	// running pod within about a minute — except under subPath, which never is.
	Mounts []Mount `json:"mounts,omitempty"`
}

// Mount is one place a ConfigMap is mounted.
type Mount struct {
	Path string `json:"path"`
	// Keys limits the mount to some keys; empty means all of them.
	Keys    []string `json:"keys,omitempty"`
	SubPath bool     `json:"subPath,omitempty"`
}

// ConfigMaps nobody edits by hand. The CA bundles are written by the cluster
// into every namespace; the requests collections are the portal's own and have
// their own screen.
var systemConfigMaps = map[string]bool{
	"kube-root-ca.crt":         true,
	"openshift-service-ca.crt": true,
	"istio-ca-root-cert":       true,
}

func systemConfigMap(name string) bool {
	return systemConfigMaps[name] || strings.HasPrefix(name, requestsPrefix)
}

// ListConfigMaps returns the ConfigMaps in a namespace, with how many
// workloads read each.
func (m *Manager) ListConfigMaps(ctx context.Context, asUser, namespace string) ([]ConfigMapSummary, error) {
	cs, err := m.Clientset(asUser)
	if err != nil {
		return nil, err
	}
	list, err := cs.CoreV1().ConfigMaps(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	// Not fatal: the list is the point, the counts a convenience.
	readers, uerr := configMapReaders(ctx, cs, namespace)

	out := make([]ConfigMapSummary, 0, len(list.Items))
	for i := range list.Items {
		c := &list.Items[i]
		s := ConfigMapSummary{
			Name:      c.Name,
			Keys:      len(c.Data) + len(c.BinaryData),
			Release:   c.Annotations["meta.helm.sh/release-name"],
			Immutable: c.Immutable != nil && *c.Immutable,
			System:    systemConfigMap(c.Name),
		}
		if uerr == nil {
			n := len(readers[c.Name])
			s.UsedBy = &n
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// GetConfigMap returns one ConfigMap with its values and its readers.
func (m *Manager) GetConfigMap(ctx context.Context, asUser, namespace, name string) (*ConfigMapData, error) {
	cs, err := m.Clientset(asUser)
	if err != nil {
		return nil, err
	}
	c, err := cs.CoreV1().ConfigMaps(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	d := configMapData(c)
	readers, uerr := configMapReaders(ctx, cs, namespace)
	if uerr != nil {
		d.UsageUnknown = true
	} else {
		d.UsedBy = readers[name]
	}
	if d.UsedBy == nil {
		d.UsedBy = []Usage{}
	}
	return d, nil
}

func configMapData(c *corev1.ConfigMap) *ConfigMapData {
	d := &ConfigMapData{
		Name:      c.Name,
		Data:      c.Data,
		Release:   c.Annotations["meta.helm.sh/release-name"],
		Immutable: c.Immutable != nil && *c.Immutable,
		Version:   c.ResourceVersion,
	}
	if d.Data == nil {
		d.Data = map[string]string{}
	}
	d.YAML = RenderConfigMapData(d.Data)
	for k := range c.BinaryData {
		d.BinaryKeys = append(d.BinaryKeys, k)
	}
	sort.Strings(d.BinaryKeys)
	return d
}

// CreateConfigMap creates a ConfigMap. It returns a description of what it
// holds — key names, never values — for the audit trail.
func (m *Manager) CreateConfigMap(ctx context.Context, asUser, namespace, name string, data map[string]string) (string, error) {
	cs, err := m.Clientset(asUser)
	if err != nil {
		return "", err
	}
	_, err = cs.CoreV1().ConfigMaps(namespace).Create(ctx, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Data:       data,
	}, metav1.CreateOptions{})
	if err != nil {
		return "", err
	}
	return changes.Keys(nil, data), nil
}

// UpdateConfigMap replaces the text values of a ConfigMap, provided nobody has
// changed it since version was read. Binary values, labels and annotations are
// kept as they are. It returns the ConfigMap as it now stands and a description
// of the change.
func (m *Manager) UpdateConfigMap(ctx context.Context, asUser, namespace, name, version string, data map[string]string) (*ConfigMapData, string, error) {
	cs, err := m.Clientset(asUser)
	if err != nil {
		return nil, "", err
	}
	return updateConfigMap(ctx, cs, namespace, name, version, data)
}

func updateConfigMap(ctx context.Context, cs kubernetes.Interface, namespace, name, version string, data map[string]string) (*ConfigMapData, string, error) {
	cms := cs.CoreV1().ConfigMaps(namespace)
	c, err := cms.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, "", err
	}
	if c.ResourceVersion != version {
		return nil, "", ErrConfigMapChanged
	}
	if c.Immutable != nil && *c.Immutable {
		return nil, "", ErrConfigMapImmutable
	}
	detail := describeConfigMap(c.Data, data)
	c.Data = data
	c, err = cms.Update(ctx, c, metav1.UpdateOptions{})
	if apierrors.IsConflict(err) {
		return nil, "", ErrConfigMapChanged
	}
	if err != nil {
		return nil, "", err
	}
	d := configMapData(c)
	if readers, rerr := configMapReaders(ctx, cs, namespace); rerr == nil {
		d.UsedBy = readers[name]
	} else {
		d.UsageUnknown = true
	}
	if d.UsedBy == nil {
		d.UsedBy = []Usage{}
	}
	return d, detail, nil
}

// DeleteConfigMap deletes a ConfigMap and says which keys went with it.
func (m *Manager) DeleteConfigMap(ctx context.Context, asUser, namespace, name string) (string, error) {
	cs, err := m.Clientset(asUser)
	if err != nil {
		return "", err
	}
	var detail string
	if c, err := cs.CoreV1().ConfigMaps(namespace).Get(ctx, name, metav1.GetOptions{}); err == nil {
		detail = changes.Keys(c.Data, nil)
	}
	return detail, cs.CoreV1().ConfigMaps(namespace).Delete(ctx, name, metav1.DeleteOptions{})
}

// ExportConfigMap returns a ConfigMap as a manifest that kubectl apply, or the
// import here, recreates elsewhere: name and data only. Labels and annotations
// belong to whatever installed it in this environment.
func (m *Manager) ExportConfigMap(ctx context.Context, asUser, namespace, name string) ([]byte, error) {
	cs, err := m.Clientset(asUser)
	if err != nil {
		return nil, err
	}
	c, err := cs.CoreV1().ConfigMaps(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	out := corev1.ConfigMap{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"},
		ObjectMeta: metav1.ObjectMeta{Name: c.Name},
		Data:       c.Data,
		BinaryData: c.BinaryData,
	}
	return yaml.Marshal(out)
}

// ParseConfigMapManifest reads a ConfigMap manifest, YAML or JSON, as written
// by ExportConfigMap or by hand.
func ParseConfigMapManifest(b []byte) (string, map[string]string, error) {
	var c corev1.ConfigMap
	if err := yaml.UnmarshalStrict(b, &c); err != nil {
		if err2 := yaml.Unmarshal(b, &c); err2 != nil {
			return "", nil, err
		}
	}
	if c.Kind != "" && c.Kind != "ConfigMap" {
		return "", nil, errors.New("this is a " + c.Kind + ", not a ConfigMap")
	}
	if c.Name == "" {
		return "", nil, errors.New("the manifest has no metadata.name")
	}
	if len(c.BinaryData) > 0 {
		return "", nil, errors.New("binary values cannot be imported here")
	}
	if c.Data == nil {
		c.Data = map[string]string{}
	}
	return c.Name, c.Data, nil
}

// describeConfigMap names what changed: keys added, changed and removed, and
// for the keys that hold files, which settings inside them. Never a value.
func describeConfigMap(before, after map[string]string) string {
	d := changes.Keys(before, after)
	var files []string
	for k, a := range after {
		b, ok := before[k]
		if ok && b != a && fileLike(k, b+a) {
			files = append(files, k+": "+changes.Document(k, b, a, true))
		}
	}
	sort.Strings(files)
	if len(files) > 0 {
		d += " — " + strings.Join(files, "; ")
	}
	return d
}

var fileExts = map[string]bool{
	".yml": true, ".yaml": true, ".json": true, ".xml": true, ".properties": true, ".conf": true,
	".cfg": true, ".ini": true, ".toml": true, ".env": true, ".sh": true, ".sql": true, ".txt": true,
	".js": true, ".lua": true, ".tpl": true, ".pem": true, ".crt": true,
}

// fileLike says whether a key holds a file rather than a single setting.
func fileLike(key, values string) bool {
	return strings.Contains(values, "\n") || fileExts[strings.ToLower(path.Ext(key))]
}

// configMapReaders maps each ConfigMap in a namespace to the workloads that
// read it, and how.
func configMapReaders(ctx context.Context, cs kubernetes.Interface, namespace string) (map[string][]Usage, error) {
	type tpl struct {
		kind, name string
		spec       corev1.PodSpec
	}
	var tpls []tpl
	deps, err := cs.AppsV1().Deployments(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	for _, d := range deps.Items {
		tpls = append(tpls, tpl{"Deployment", d.Name, d.Spec.Template.Spec})
	}
	if l, err := cs.AppsV1().StatefulSets(namespace).List(ctx, metav1.ListOptions{}); err == nil {
		for _, d := range l.Items {
			tpls = append(tpls, tpl{"StatefulSet", d.Name, d.Spec.Template.Spec})
		}
	}
	if l, err := cs.AppsV1().DaemonSets(namespace).List(ctx, metav1.ListOptions{}); err == nil {
		for _, d := range l.Items {
			tpls = append(tpls, tpl{"DaemonSet", d.Name, d.Spec.Template.Spec})
		}
	}
	if l, err := cs.BatchV1().CronJobs(namespace).List(ctx, metav1.ListOptions{}); err == nil {
		for _, d := range l.Items {
			tpls = append(tpls, tpl{"CronJob", d.Name, d.Spec.JobTemplate.Spec.Template.Spec})
		}
	}

	out := map[string][]Usage{}
	for _, t := range tpls {
		for cm, u := range readsOf(t.spec) {
			u.Kind, u.Name = t.kind, t.name
			out[cm] = append(out[cm], u)
		}
	}
	return out, nil
}

// readsOf finds every ConfigMap a pod spec reads, and how.
func readsOf(spec corev1.PodSpec) map[string]Usage {
	uses := map[string]*Usage{}
	use := func(cm string) *Usage {
		if uses[cm] == nil {
			uses[cm] = &Usage{}
		}
		return uses[cm]
	}

	// Which volume carries which ConfigMap, and which keys of it.
	type src struct {
		cm   string
		keys []string
	}
	volumes := map[string][]src{}
	for _, v := range spec.Volumes {
		if v.ConfigMap != nil {
			volumes[v.Name] = append(volumes[v.Name], src{v.ConfigMap.Name, itemKeys(v.ConfigMap.Items)})
		}
		if v.Projected != nil {
			for _, s := range v.Projected.Sources {
				if s.ConfigMap != nil {
					volumes[v.Name] = append(volumes[v.Name], src{s.ConfigMap.Name, itemKeys(s.ConfigMap.Items)})
				}
			}
		}
	}

	containers := append(append([]corev1.Container{}, spec.InitContainers...), spec.Containers...)
	seenMount := map[string]bool{}
	for _, c := range containers {
		for _, e := range c.EnvFrom {
			if e.ConfigMapRef != nil {
				use(e.ConfigMapRef.Name).EnvAll = true
			}
		}
		for _, e := range c.Env {
			if e.ValueFrom != nil && e.ValueFrom.ConfigMapKeyRef != nil {
				u := use(e.ValueFrom.ConfigMapKeyRef.Name)
				u.Env = appendNew(u.Env, e.ValueFrom.ConfigMapKeyRef.Key)
			}
		}
		for _, vm := range c.VolumeMounts {
			for _, s := range volumes[vm.Name] {
				p := vm.MountPath
				keys := s.keys
				if vm.SubPath != "" {
					keys = []string{vm.SubPath}
				}
				if id := s.cm + "\x00" + p; !seenMount[id] {
					seenMount[id] = true
					u := use(s.cm)
					u.Mounts = append(u.Mounts, Mount{Path: p, Keys: keys, SubPath: vm.SubPath != ""})
				}
			}
		}
	}

	out := make(map[string]Usage, len(uses))
	for cm, u := range uses {
		sort.Strings(u.Env)
		out[cm] = *u
	}
	return out
}

func itemKeys(items []corev1.KeyToPath) []string {
	var keys []string
	for _, it := range items {
		keys = append(keys, it.Key)
	}
	return keys
}

func appendNew(list []string, s string) []string {
	for _, x := range list {
		if x == s {
			return list
		}
	}
	return append(list, s)
}
