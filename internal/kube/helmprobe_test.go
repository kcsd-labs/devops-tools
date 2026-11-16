package kube

import (
	"context"
	"strings"
	"testing"

	"helm.sh/helm/v3/pkg/chart"
	"k8s.io/client-go/rest"
)

// A chart that stops at the first value it lacks is asked again with a
// stand-in for it, until it has said everything it needs.
func TestAskAllAtOnce(t *testing.T) {
	ch := &chart.Chart{
		Metadata: &chart.Metadata{APIVersion: "v2", Name: "app", Version: "1.0.0"},
		Templates: []*chart.File{{Name: "templates/cm.yaml", Data: []byte(`apiVersion: v1
kind: ConfigMap
metadata: {name: x}
data:
  host: {{ required "You must configure a database. Please set db.host" .Values.db.host }}
  secret: {{ required "Please set db.password.secret" .Values.db.password.secret }}
  email: {{ required "A value for mail.from is required" .Values.mail.from }}
`)}},
		Values: map[string]any{},
	}
	m := &Manager{config: &rest.Config{Host: "http://127.0.0.1:1"}}
	vals := map[string]any{"db": map[string]any{"port": 5432}}

	_, err := m.renderOffline("team", "app", ch, vals)
	if err == nil {
		t.Fatal("the chart should refuse")
	}
	plan := &ReleasePlan{}
	plan.notRendered(err, ch)
	if len(plan.Required) != 1 {
		t.Fatalf("one at a time from helm: %+v", plan.Required)
	}
	m.askAllAtOnce(context.Background(), plan, "team", "app", ch, vals)
	var keys []string
	for _, r := range plan.Required {
		keys = append(keys, r.Key)
	}
	if strings.Join(keys, ",") != "db.host,db.password.secret,mail.from" {
		t.Errorf("required = %v", keys)
	}
	if _, touched := vals["mail"]; touched || len(vals["db"].(map[string]any)) != 1 {
		t.Errorf("the probe must not touch the real values: %v", vals)
	}
}
