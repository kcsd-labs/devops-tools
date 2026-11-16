package kube

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestParseRefusal(t *testing.T) {
	msg := "INSTALLATION FAILED: execution error at (gitlab/templates/NOTES.txt:251:3): \n" +
		"CONFIGURATION CHECKS:\n\n" +
		"Registry Object Storage:\n    The chart provides no longer bundled object storage solution.\n    Please prepare an external one.\n\n" +
		"Object Storage:\n    When type-specific object storage is enabled the `connection` property can not be empty.\n"
	r := parseRefusal(msg)
	if r.Where != "gitlab/templates/NOTES.txt:251:3" {
		t.Errorf("where = %q", r.Where)
	}
	want := []string{
		"Registry Object Storage: The chart provides no longer bundled object storage solution. Please prepare an external one.",
		"Object Storage: When type-specific object storage is enabled the `connection` property can not be empty.",
	}
	if len(r.Messages) != len(want) {
		t.Fatalf("messages = %q", r.Messages)
	}
	for i := range want {
		if r.Messages[i] != want[i] {
			t.Errorf("message %d = %q, want %q", i, r.Messages[i], want[i])
		}
	}

	if r := parseRefusal("template: app/templates/x.yaml:3:5: unexpected EOF"); len(r.Messages) != 1 || r.Where != "" {
		t.Errorf("a plain error = %+v", r)
	}
}

func TestSettingSecrets(t *testing.T) {
	got := settingSecrets([]corev1.Secret{
		{ObjectMeta: metav1.ObjectMeta{Name: "psql"}, Type: corev1.SecretTypeOpaque, Data: map[string][]byte{"password": nil, "user": nil}},
		{ObjectMeta: metav1.ObjectMeta{Name: "sh.helm.release.v1.app.v1"}, Type: "helm.sh/release.v1"},
		{ObjectMeta: metav1.ObjectMeta{Name: "wildcard"}, Type: corev1.SecretTypeTLS},
		{ObjectMeta: metav1.ObjectMeta{Name: "basic"}, Type: corev1.SecretTypeBasicAuth, Data: map[string][]byte{"username": nil}},
	})
	if len(got) != 2 || got[0].Name != "basic" || got[1].Name != "psql" || len(got[1].Keys) != 2 || got[1].Keys[0] != "password" {
		t.Errorf("secrets = %+v", got)
	}
}
