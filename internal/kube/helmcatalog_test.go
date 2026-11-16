package kube

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/yaml"
)

func testMapper() meta.RESTMapper {
	m := meta.NewDefaultRESTMapper(nil)
	ns := []schema.GroupVersionKind{
		{Version: "v1", Kind: "ConfigMap"}, {Version: "v1", Kind: "Service"}, {Version: "v1", Kind: "Pod"},
		{Group: "apps", Version: "v1", Kind: "StatefulSet"}, {Group: "apps", Version: "v1", Kind: "DaemonSet"},
		{Group: "batch", Version: "v1", Kind: "CronJob"},
		{Group: "rbac.authorization.k8s.io", Version: "v1", Kind: "RoleBinding"},
	}
	for _, g := range ns {
		m.Add(g, meta.RESTScopeNamespace)
	}
	m.Add(schema.GroupVersionKind{Group: "rbac.authorization.k8s.io", Version: "v1", Kind: "ClusterRole"}, meta.RESTScopeRoot)
	return m
}

func planOf(t *testing.T, docs ...string) *ReleasePlan {
	t.Helper()
	plan := &ReleasePlan{}
	for _, d := range docs {
		var obj map[string]any
		if err := yaml.Unmarshal([]byte(d), &obj); err != nil {
			t.Fatal(err)
		}
		checkObject(plan, testMapper(), "payments-dev", &unstructured.Unstructured{Object: obj}, false)
	}
	return plan
}

func problemsText(p *ReleasePlan) string {
	var out []string
	for _, pr := range p.Problems {
		out = append(out, pr.Level+": "+pr.Text)
	}
	return strings.Join(out, "\n")
}

func TestPlanAcceptsAnOrdinaryChart(t *testing.T) {
	p := planOf(t, `
apiVersion: apps/v1
kind: StatefulSet
metadata: {name: redis-master}
spec:
  template:
    spec:
      initContainers: [{name: init, image: busybox:1.37}]
      containers: [{name: redis, image: bitnami/redis:8.2}]
      volumes: [{name: tz, hostPath: {path: /etc/localtime}}]
`, `
apiVersion: v1
kind: ConfigMap
metadata: {name: redis-config, namespace: payments-dev}
`, `
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata: {name: redis}
roleRef: {kind: ClusterRole, name: view}
`)
	if !p.OK() || len(p.Problems) != 0 {
		t.Fatalf("an ordinary chart should pass:\n%s", problemsText(p))
	}
	if len(p.Objects) != 3 || len(p.Images) != 2 {
		t.Errorf("objects %v, images %v", p.Objects, p.Images)
	}
}

func TestPlanRefusesWhatReachesPastTheNamespace(t *testing.T) {
	cases := map[string]string{
		"cluster-wide": `
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata: {name: everything}`,
		"another namespace": `
apiVersion: v1
kind: ConfigMap
metadata: {name: x, namespace: kube-system}`,
		"privileged": `
apiVersion: apps/v1
kind: DaemonSet
metadata: {name: agent}
spec: {template: {spec: {containers: [{name: a, image: x, securityContext: {privileged: true}}]}}}`,
		"privileged init": `
apiVersion: v1
kind: Pod
metadata: {name: p}
spec: {initContainers: [{name: i, image: x, securityContext: {privileged: true}}], containers: [{name: a, image: x}]}`,
		"node directory": `
apiVersion: batch/v1
kind: CronJob
metadata: {name: c}
spec: {jobTemplate: {spec: {template: {spec: {containers: [{name: a, image: x}], volumes: [{name: r, hostPath: {path: /}}]}}}}}`,
		"host network": `
apiVersion: v1
kind: Pod
metadata: {name: p}
spec: {hostNetwork: true, containers: [{name: a, image: x}]}`,
		"admin binding": `
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata: {name: b}
roleRef: {kind: ClusterRole, name: cluster-admin}`,
		"unknown kind": `
apiVersion: example.com/v1
kind: Widget
metadata: {name: w}`,
	}
	for name, doc := range cases {
		if p := planOf(t, doc); p.OK() {
			t.Errorf("%s: should be refused", name)
		}
	}
}

func TestPlanWarnsAboutNodePorts(t *testing.T) {
	p := planOf(t, `
apiVersion: v1
kind: Service
metadata: {name: s}
spec: {type: NodePort}`)
	if !p.OK() || len(p.Problems) != 1 || p.Problems[0].Level != "warn" {
		t.Errorf("a NodePort is a warning, not a refusal:\n%s", problemsText(p))
	}
}

func TestCatalogLabels(t *testing.T) {
	if l := CatalogLabels("bitnami", "redis"); l[labelCatalogRepo] != "bitnami" || l[labelCatalogChart] != "redis" {
		t.Errorf("labels = %v", l)
	}
	if l := CatalogLabels("bad repo", "redis"); l != nil {
		t.Errorf("a value that is not a label value gives no labels, got %v", l)
	}
}
