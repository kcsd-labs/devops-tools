package helmcat

import (
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

func values(t *testing.T, text string) map[string]any {
	t.Helper()
	m := map[string]any{}
	if err := yaml.Unmarshal([]byte(text), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// As Bitnami writes them: a global storage class, and an ingress by hostname.
const bitnamiValues = `
global:
  imageRegistry: ""
  defaultStorageClass: ""
master:
  persistence:
    enabled: true
    storageClass: ""
replica:
  persistence:
    storageClass: ""
ingress:
  enabled: false
  hostname: redis.local
  ingressClassName: ""
  tls: false
  extraTls: []
`

// As helm create writes them, with a component's own ingress below.
const createValues = `
persistence:
  storageClassName: ""
ingress:
  enabled: false
  className: ""
  hosts:
    - host: chart-example.local
      paths: [{path: /, pathType: ImplementationSpecific}]
  tls: []
server:
  ingress:
    enabled: false
    hosts: []
metrics:
  ingress:
    annotations: {}
grafana:
  ingress:
    enabled: false
    ingressClassName: ""
    hosts: [chart-example.local]
    path: /
alertmanager:
  ingress:
    enabled: false
    hosts: []
    paths: []
`

func TestDetect(t *testing.T) {
	b := Detect(values(t, bitnamiValues))
	if strings.Join(b.Storage, ",") != "global.defaultStorageClass" {
		t.Errorf("bitnami storage = %v; the global one is enough", b.Storage)
	}
	if len(b.Ingresses) != 1 || b.Ingresses[0] != (IngressSpot{Path: "ingress", Shape: "hostname", ClassKey: "ingressClassName"}) {
		t.Errorf("bitnami ingresses = %+v", b.Ingresses)
	}

	c := Detect(values(t, createValues))
	if strings.Join(c.Storage, ",") != "persistence.storageClassName" {
		t.Errorf("create storage = %v", c.Storage)
	}
	want := []IngressSpot{
		{Path: "ingress", Shape: "hosts", ClassKey: "className"},
		{Path: "alertmanager.ingress", Shape: "hostlist", ClassKey: "ingressClassName"},
		{Path: "grafana.ingress", Shape: "hostlist", ClassKey: "ingressClassName"},
		{Path: "server.ingress", Shape: "hosts", ClassKey: "className"},
	}
	if len(c.Ingresses) != len(want) {
		t.Fatalf("create ingresses = %+v (metrics.ingress has no enabled and is not one)", c.Ingresses)
	}
	for i := range want {
		if c.Ingresses[i] != want[i] {
			t.Errorf("ingress %d = %+v, want %+v", i, c.Ingresses[i], want[i])
		}
	}
}

func TestApplyFill(t *testing.T) {
	out, _, err := ApplyFill("master:\n  resources:\n    limits: {memory: 512Mi}\n", Fill{
		StorageClass: "local-path", StoragePaths: []string{"global.defaultStorageClass"},
		Ingress: &IngressFill{IngressSpot: IngressSpot{Path: "ingress", Shape: "hostname", ClassKey: "ingressClassName"},
			Host: "redis.dev.example.com", Class: "nginx", TLS: "wildcard-tls"},
	}, Fill{})
	if err != nil {
		t.Fatal(err)
	}
	got := values(t, out)
	if got["global"].(map[string]any)["defaultStorageClass"] != "local-path" {
		t.Errorf("storage class not set:\n%s", out)
	}
	ing := got["ingress"].(map[string]any)
	if ing["enabled"] != true || ing["hostname"] != "redis.dev.example.com" || ing["ingressClassName"] != "nginx" {
		t.Errorf("ingress = %v", ing)
	}
	if tls := ing["extraTls"].([]any)[0].(map[string]any); tls["secretName"] != "wildcard-tls" {
		t.Errorf("tls = %v", tls)
	}
	if got["master"].(map[string]any)["resources"] == nil {
		t.Error("what was in the values already must stay")
	}

	out, _, err = ApplyFill("", Fill{Ingress: &IngressFill{IngressSpot: IngressSpot{Path: "server.ingress", Shape: "hosts", ClassKey: "className"},
		Host: "app.example.com"}}, Fill{})
	if err != nil {
		t.Fatal(err)
	}
	ing = values(t, out)["server"].(map[string]any)["ingress"].(map[string]any)
	if h := ing["hosts"].([]any)[0].(map[string]any); h["host"] != "app.example.com" || ing["tls"] != nil || ing["className"] != nil {
		t.Errorf("hosts-shaped ingress without class or tls = %v", ing)
	}

	if _, _, err := ApplyFill("ingress: off\n", Fill{Ingress: &IngressFill{IngressSpot: IngressSpot{Path: "ingress", Shape: "hosts"}, Host: "a.b"}}, Fill{}); err == nil {
		t.Error("a value that is not a map should not be silently replaced")
	}
}

// The form sends every change: what it wrote before comes out again, what was
// changed by hand stays, and the rest of the values — comments too — is kept.
func TestApplyFillFollowsTheForm(t *testing.T) {
	spot := IngressSpot{Path: "ingress", Shape: "hostname", ClassKey: "ingressClassName"}
	one := Fill{StorageClass: "nfs", StoragePaths: []string{"global.defaultStorageClass"},
		Ingress: &IngressFill{IngressSpot: spot, Host: "a.example.com", Class: "nginx"}}
	start := "# my settings\nreplicaCount: 2 # two is enough\n"

	out, kept, err := ApplyFill(start, one, Fill{})
	if err != nil || len(kept) != 0 {
		t.Fatal(err, kept)
	}
	if !strings.Contains(out, "# my settings") || !strings.Contains(out, "# two is enough") {
		t.Errorf("comments lost:\n%s", out)
	}

	// The ingress unticked: its keys go, and the map they were in.
	two := Fill{StorageClass: "nfs", StoragePaths: one.StoragePaths}
	out2, _, err := ApplyFill(out, two, one)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out2, "ingress") || !strings.Contains(out2, "defaultStorageClass: nfs") {
		t.Errorf("unticking the ingress:\n%s", out2)
	}

	// The host changed by hand, then the class in the form: the host stays.
	edited := strings.Replace(out, "hostname: a.example.com", "hostname: mine.example.com", 1)
	three := one
	three.Ingress = &IngressFill{IngressSpot: spot, Host: "a.example.com", Class: "traefik"}
	out3, kept, err := ApplyFill(edited, three, one)
	if err != nil {
		t.Fatal(err)
	}
	ing := values(t, out3)["ingress"].(map[string]any)
	if ing["hostname"] != "mine.example.com" || ing["ingressClassName"] != "traefik" {
		t.Errorf("ingress = %v", ing)
	}
	if strings.Join(kept, ",") != "ingress.hostname" {
		t.Errorf("kept = %v", kept)
	}

	// Everything off again: back to what was typed.
	out4, _, err := ApplyFill(out, Fill{}, one)
	if err != nil {
		t.Fatal(err)
	}
	if v := values(t, out4); len(v) != 1 || v["replicaCount"] != float64(2) {
		t.Errorf("all off:\n%s", out4)
	}
}
