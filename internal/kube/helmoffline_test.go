package kube

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"helm.sh/helm/v3/pkg/chart"
	"k8s.io/client-go/rest"
)

// Rendered without the cluster, a chart still sees the cluster's version:
// left to itself Helm says 1.20, and a chart wanting a newer one refuses.
func TestRenderOfflineKnowsTheClusterVersion(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/version":
			_, _ = w.Write([]byte(`{"major":"1","minor":"30","gitVersion":"v1.30.4"}`))
		case "/api":
			_, _ = w.Write([]byte(`{"kind":"APIVersions","versions":["v1"]}`))
		case "/apis":
			_, _ = w.Write([]byte(`{"kind":"APIGroupList","groups":[]}`))
		case "/api/v1":
			_, _ = w.Write([]byte(`{"kind":"APIResourceList","groupVersion":"v1","resources":[{"name":"configmaps","namespaced":true,"kind":"ConfigMap","verbs":["get"]}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	ch := &chart.Chart{
		Metadata: &chart.Metadata{APIVersion: "v2", Name: "app", Version: "1.0.0", KubeVersion: ">=1.25.0-0"},
		Templates: []*chart.File{{Name: "templates/cm.yaml", Data: []byte(`apiVersion: v1
kind: ConfigMap
metadata: {name: x}
data: {kube: "{{ .Capabilities.KubeVersion.Minor }}"}
`)}},
	}
	m := &Manager{config: &rest.Config{Host: srv.URL}}
	rel, err := m.renderOffline("team", "app", ch, map[string]any{})
	if err != nil {
		t.Fatalf("the cluster is 1.30: %v", err)
	}
	if want := `kube: "30"`; !strings.Contains(rel.Manifest, want) {
		t.Errorf("manifest = %s", rel.Manifest)
	}
}
