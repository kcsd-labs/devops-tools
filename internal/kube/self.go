package kube

import (
	"context"
	"fmt"
	"io"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// PodIdentity describes the pod this process runs in. It is what lets the
// service find its own log stream — in Loki by label, or through the API when
// there is no Loki.
type PodIdentity struct {
	Namespace string
	App       string // app.kubernetes.io/name, as a log collector usually records it
	Selector  string // matches the other replicas of this release
}

// Identify reads the running pod to learn how it is labelled.
//
// Name and namespace come from the downward API; everything else is read from
// the pod itself, so a renamed release or a relabelled chart needs no second
// setting to keep in step.
func (m *Manager) Identify(ctx context.Context, namespace, pod string) (PodIdentity, error) {
	if namespace == "" || pod == "" {
		return PodIdentity{}, fmt.Errorf("pod name and namespace are not set: not running in a cluster")
	}
	cs, err := m.Clientset("")
	if err != nil {
		return PodIdentity{}, err
	}
	p, err := cs.CoreV1().Pods(namespace).Get(ctx, pod, metav1.GetOptions{})
	if err != nil {
		return PodIdentity{}, fmt.Errorf("read own pod %s/%s: %w", namespace, pod, err)
	}
	id := PodIdentity{Namespace: namespace, App: p.Labels["app.kubernetes.io/name"]}
	if id.App == "" {
		id.App = p.Labels["app"]
	}
	if instance := p.Labels["app.kubernetes.io/instance"]; id.App != "" && instance != "" {
		id.Selector = fmt.Sprintf("app.kubernetes.io/name=%s,app.kubernetes.io/instance=%s", id.App, instance)
	}
	return id, nil
}

// OwnPods lists the service's own pods.
//
// Not impersonated, here and in OwnPodLogs: reading the trail the service
// itself wrote is gated by the portal's roles, not by what the signed-in person
// happens to be allowed in Kubernetes.
func (m *Manager) OwnPods(ctx context.Context, namespace, selector string) ([]string, error) {
	cs, err := m.Clientset("")
	if err != nil {
		return nil, err
	}
	list, err := cs.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(list.Items))
	for _, p := range list.Items {
		names = append(names, p.Name)
	}
	return names, nil
}

// OwnPodLogs streams one of those pods from a point in time.
func (m *Manager) OwnPodLogs(ctx context.Context, namespace, pod string, since time.Time, previous bool) (io.ReadCloser, error) {
	cs, err := m.Clientset("")
	if err != nil {
		return nil, err
	}
	opts := &corev1.PodLogOptions{Previous: previous}
	if !since.IsZero() {
		t := metav1.NewTime(since)
		opts.SinceTime = &t
	}
	return cs.CoreV1().Pods(namespace).GetLogs(pod, opts).Stream(ctx)
}
