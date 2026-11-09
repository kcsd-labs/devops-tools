package kube

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestEveryWayOfReadingAConfigMapIsFound(t *testing.T) {
	spec := corev1.PodSpec{
		Volumes: []corev1.Volume{
			{Name: "conf", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
				LocalObjectReference: corev1.LocalObjectReference{Name: "app-config"},
				Items:                []corev1.KeyToPath{{Key: "application.yml", Path: "application.yml"}},
			}}},
			{Name: "logback", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
				LocalObjectReference: corev1.LocalObjectReference{Name: "app-config"},
			}}},
			{Name: "bundle", VolumeSource: corev1.VolumeSource{Projected: &corev1.ProjectedVolumeSource{
				Sources: []corev1.VolumeProjection{{ConfigMap: &corev1.ConfigMapProjection{
					LocalObjectReference: corev1.LocalObjectReference{Name: "ca-bundle"},
				}}},
			}}},
		},
		InitContainers: []corev1.Container{{
			EnvFrom: []corev1.EnvFromSource{{ConfigMapRef: &corev1.ConfigMapEnvSource{
				LocalObjectReference: corev1.LocalObjectReference{Name: "flags"},
			}}},
		}},
		Containers: []corev1.Container{{
			Env: []corev1.EnvVar{
				{Name: "LOG_LEVEL", ValueFrom: &corev1.EnvVarSource{ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: "app-config"}, Key: "LOG_LEVEL",
				}}},
				{Name: "PLAIN", Value: "x"},
			},
			VolumeMounts: []corev1.VolumeMount{
				{Name: "conf", MountPath: "/config"},
				{Name: "logback", MountPath: "/app/logback.xml", SubPath: "logback.xml"},
				{Name: "bundle", MountPath: "/etc/ssl/extra"},
			},
		}},
	}
	got := readsOf(spec)

	want := Usage{
		Env: []string{"LOG_LEVEL"},
		Mounts: []Mount{
			{Path: "/config", Keys: []string{"application.yml"}},
			{Path: "/app/logback.xml", Keys: []string{"logback.xml"}, SubPath: true},
		},
	}
	if !reflect.DeepEqual(got["app-config"], want) {
		t.Errorf("app-config = %+v, want %+v", got["app-config"], want)
	}
	if !got["flags"].EnvAll {
		t.Errorf("flags = %+v, want envFrom", got["flags"])
	}
	if m := got["ca-bundle"].Mounts; len(m) != 1 || m[0].Path != "/etc/ssl/extra" {
		t.Errorf("ca-bundle = %+v, want the projected mount", got["ca-bundle"])
	}
}

func TestAChangeIsDescribedByKeyAndSettingNeverByValue(t *testing.T) {
	before := map[string]string{
		"application.yml": "server:\n  port: 8080\npool:\n  max: 10\n",
		"LOG_LEVEL":       "INFO",
		"OLD":             "x",
	}
	after := map[string]string{
		"application.yml": "server:\n  port: 8080\npool:\n  max: 20\n",
		"LOG_LEVEL":       "DEBUG",
		"NEW":             "hunter2",
	}
	got := describeConfigMap(before, after)
	for _, want := range []string{"added NEW", "LOG_LEVEL", "application.yml", "removed OLD", "application.yml: changed pool.max"} {
		if !strings.Contains(got, want) {
			t.Errorf("detail %q lacks %q", got, want)
		}
	}
	for _, value := range []string{"DEBUG", "INFO", "hunter2", "20"} {
		if strings.Contains(got, value) {
			t.Errorf("detail %q contains the value %q", got, value)
		}
	}
}

func TestAStaleOrImmutableConfigMapIsNotOverwritten(t *testing.T) {
	ctx := context.Background()
	yes := true
	cs := fake.NewSimpleClientset(
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: testNS, ResourceVersion: "5"},
			Data: map[string]string{"A": "1"}, BinaryData: map[string][]byte{"blob": {0, 1}}},
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "frozen", Namespace: testNS, ResourceVersion: "2"},
			Immutable: &yes},
	)
	if _, _, err := updateConfigMap(ctx, cs, testNS, "app", "4", map[string]string{"A": "2"}); !errors.Is(err, ErrConfigMapChanged) {
		t.Errorf("stale version: err = %v", err)
	}
	if _, _, err := updateConfigMap(ctx, cs, testNS, "frozen", "2", map[string]string{"A": "2"}); !errors.Is(err, ErrConfigMapImmutable) {
		t.Errorf("immutable: err = %v", err)
	}

	d, detail, err := updateConfigMap(ctx, cs, testNS, "app", "5", map[string]string{"A": "2", "B": "3"})
	if err != nil {
		t.Fatal(err)
	}
	if d.Data["A"] != "2" || !reflect.DeepEqual(d.BinaryKeys, []string{"blob"}) || detail == "" {
		t.Errorf("after save: %+v, %q", d, detail)
	}
}

func TestReadersAreCountedPerConfigMap(t *testing.T) {
	ctx := context.Background()
	dep := func(name, cm string) *appsv1.Deployment {
		return &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNS},
			Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
				Containers: []corev1.Container{{EnvFrom: []corev1.EnvFromSource{{ConfigMapRef: &corev1.ConfigMapEnvSource{
					LocalObjectReference: corev1.LocalObjectReference{Name: cm},
				}}}}},
			}}},
		}
	}
	cs := fake.NewSimpleClientset(
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "flags", Namespace: testNS}},
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "orphan", Namespace: testNS}},
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "kube-root-ca.crt", Namespace: testNS}},
		dep("api", "flags"), dep("worker", "flags"),
	)
	readers, err := configMapReaders(ctx, cs, testNS)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(readers["flags"]); n != 2 {
		t.Errorf("flags has %d readers, want 2", n)
	}
	if n := len(readers["orphan"]); n != 0 {
		t.Errorf("orphan has %d readers, want 0", n)
	}
	if !systemConfigMap("kube-root-ca.crt") || !systemConfigMap("devops-tools-requests-api") || systemConfigMap("flags") {
		t.Error("system ConfigMaps are not told apart")
	}
}

func TestAManifestIsReadBack(t *testing.T) {
	name, data, err := ParseConfigMapManifest([]byte("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: flags\ndata:\n  A: \"1\"\n"))
	if err != nil || name != "flags" || data["A"] != "1" {
		t.Fatalf("got %q %v %v", name, data, err)
	}
	if _, _, err := ParseConfigMapManifest([]byte("kind: Secret\nmetadata:\n  name: x\n")); err == nil {
		t.Error("a Secret was taken for a ConfigMap")
	}
	if _, _, err := ParseConfigMapManifest([]byte("data:\n  A: b\n")); err == nil {
		t.Error("a manifest without a name was accepted")
	}
}
