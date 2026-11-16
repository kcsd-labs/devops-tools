package helmcat

import (
	"strings"
	"testing"
)

const documented = `
## @section Global parameters
## @param global.imageRegistry Global Docker image registry
##
global:
  imageRegistry: ""
# -- Number of pods to run
replicaCount: 1
image:
  # The image to run.
  # Pinned, so upgrades are deliberate.
  repository: nginx
  tag: "" # defaults to the chart's appVersion
service:
  # type: LoadBalancer
  port: 80
`

func TestDocs(t *testing.T) {
	d := Docs([]byte(documented))
	want := map[string]string{
		"global.imageRegistry": "Global Docker image registry",
		"replicaCount":         "Number of pods to run",
		"image.repository":     "The image to run. Pinned, so upgrades are deliberate.",
		"image.tag":            "defaults to the chart's appVersion",
		"service.port":         "",
	}
	for k, v := range want {
		if got, ok := d[k]; !ok || got != v {
			t.Errorf("%s = %q (%v), want %q", k, got, ok, v)
		}
	}
	if _, ok := d["service"]; !ok {
		t.Error("a map key is a key too")
	}
}

const commented = `
global:
  psql:
    knownDecompositions:
      - main
    password: {}
      # useSecret:
      # secret:
      # key:
    # host: postgresql.hostedsomewhere.else
    # port: 123
    # username: gitlab
    # Note: this is about something else entirely
  redis:
    auth:
      enabled: true
      # secret:
    # host: redis.hostedsomewhere.else
`

func TestExamples(t *testing.T) {
	e := Examples([]byte(commented))
	want := map[string]string{
		"global.psql.password.useSecret": "",
		"global.psql.password.secret":    "",
		"global.psql.password.key":       "",
		"global.psql.host":               "postgresql.hostedsomewhere.else",
		"global.psql.port":               "123",
		"global.psql.username":           "gitlab",
		"global.redis.auth.secret":       "",
		"global.redis.host":              "redis.hostedsomewhere.else",
	}
	for k, v := range want {
		if got, ok := e[k]; !ok || got != v {
			t.Errorf("%s = %q (%v), want %q", k, got, ok, v)
		}
	}
	if len(e) != len(want) {
		t.Errorf("examples = %v", e)
	}

	out, _, err := ApplyFill("", Fill{Set: map[string]string{"global.psql.host": "db"},
		Extra: map[string]string{"global.psql.username": "app", "global.psql.host": "ignored"}}, Fill{})
	if err != nil {
		t.Fatal(err)
	}
	psql := values(t, out)["global"].(map[string]any)["psql"].(map[string]any)
	if psql["host"] != "db" || psql["username"] != "app" || !strings.Contains(out, "# from the install form") {
		t.Errorf("extra:\n%s", out)
	}
}

func TestDetectMore(t *testing.T) {
	s := Detect(values(t, `
global:
  hosts: {domain: example.com}
  ingress: {class: nginx}
replicaCount: 2
primary:
  resourcesPreset: nano
  persistence: {size: 8Gi}
server:
  persistentVolume: {size: 4Gi}
  replicas: 1
deep:
  er:
    replicaCount: 3
`))
	if len(s.Ingresses) == 0 || s.Ingresses[0] != (IngressSpot{Path: "global", Shape: "domain", ClassKey: "class"}) {
		t.Errorf("GitLab's domain: %+v", s.Ingresses)
	}
	if strings.Join(s.Presets, ",") != "primary.resourcesPreset" || s.Defaults["primary.resourcesPreset"] != "nano" {
		t.Errorf("presets %v %v", s.Presets, s.Defaults)
	}
	if strings.Join(s.Disks, ",") != "primary.persistence.size,server.persistentVolume.size" {
		t.Errorf("disks %v", s.Disks)
	}
	if strings.Join(s.Replicas, ",") != "replicaCount,server.replicas" || s.Defaults["replicaCount"] != "2" {
		t.Errorf("replicas %v (deeper ones are a part's own business)", s.Replicas)
	}
}

func TestApplyFillMore(t *testing.T) {
	out, _, err := ApplyFill("", Fill{
		Preset: "small", PresetPaths: []string{"primary.resourcesPreset"},
		Disk: "20Gi", DiskPaths: []string{"primary.persistence.size"},
		Replicas: map[string]int{"replicaCount": 3},
		Set:      map[string]string{"certmanager-issuer.email": "ops@example.com", "auth.enabled": "true", "empty": " "},
		Ingress: &IngressFill{IngressSpot: IngressSpot{Path: "global", Shape: "domain", ClassKey: "class"},
			Host: "dev.example.com", Class: "nginx", TLS: "wildcard"},
	}, Fill{})
	if err != nil {
		t.Fatal(err)
	}
	v := values(t, out)
	if v["replicaCount"] != float64(3) || v["auth"].(map[string]any)["enabled"] != true {
		t.Errorf("typed values:\n%s", out)
	}
	if v["global"].(map[string]any)["hosts"].(map[string]any)["domain"] != "dev.example.com" ||
		v["global"].(map[string]any)["ingress"].(map[string]any)["configureCertmanager"] != false {
		t.Errorf("GitLab's domain:\n%s", out)
	}
	if _, ok := v["empty"]; ok {
		t.Error("an empty field writes nothing")
	}
	for _, note := range []string{"# the chart asks for this", "# size, from the install form", "# replicas, from the install form"} {
		if !strings.Contains(out, note) {
			t.Errorf("no %q:\n%s", note, out)
		}
	}

	for _, bad := range []Fill{{Disk: "20GB", DiskPaths: []string{"a.size"}}, {Preset: "huge", PresetPaths: []string{"a"}},
		{Replicas: map[string]int{"r": 500}}, {Set: map[string]string{"a..b": "x"}}} {
		if _, _, err := ApplyFill("", bad, Fill{}); err == nil {
			t.Errorf("%+v should not apply", bad)
		}
	}
}
