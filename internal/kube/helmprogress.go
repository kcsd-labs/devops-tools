package kube

import (
	"context"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// InstallProgress is how far a release's pods have come: what someone
// waiting on an install sees instead of a spinner, and why the slow ones are.
type InstallProgress struct {
	Ready int `json:"ready"`
	Pods  int `json:"pods"`
	// Trouble is the first few pods that are not up, each with what holds
	// it: not placed, an image, a crash, a probe that fails.
	Trouble []string `json:"trouble,omitempty"`
}

// ReleaseProgress reads a release's pods with the portal's account. Nil when
// they cannot be read.
func (m *Manager) ReleaseProgress(ctx context.Context, namespace, name string) *InstallProgress {
	cs, err := m.Clientset("")
	if err != nil {
		return nil
	}
	pods, err := cs.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: "app.kubernetes.io/instance=" + name,
	})
	if err != nil {
		return nil
	}
	p := &InstallProgress{}
	items := pods.Items
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	for i := range items {
		pod := &items[i]
		if pod.Status.Phase == corev1.PodSucceeded {
			// A job that did its work.
			continue
		}
		p.Pods++
		if podReady(pod) {
			p.Ready++
			continue
		}
		if len(p.Trouble) == 3 {
			continue
		}
		why := podHold(pod)
		if why == "" {
			why = m.lastWarning(ctx, namespace, pod.Name)
		}
		if why != "" {
			p.Trouble = append(p.Trouble, pod.Name+": "+why)
		}
	}
	return p
}

// ReleaseTrouble says why a release's pods are not up, in one line; empty
// when there is nothing to say.
func (m *Manager) ReleaseTrouble(ctx context.Context, namespace, name string) string {
	p := m.ReleaseProgress(ctx, namespace, name)
	if p == nil {
		return ""
	}
	return strings.Join(p.Trouble, "; ")
}

// podHold is what the pod's own status says holds it, or "".
func podHold(p *corev1.Pod) string {
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodScheduled && c.Status == corev1.ConditionFalse && c.Message != "" {
			return "not placed on a node — " + shorten(c.Message, 200)
		}
	}
	for _, st := range append(append([]corev1.ContainerStatus{}, p.Status.InitContainerStatuses...), p.Status.ContainerStatuses...) {
		w := st.State.Waiting
		if w == nil || w.Reason == "" || w.Reason == "PodInitializing" || w.Reason == "ContainerCreating" {
			continue
		}
		msg := w.Reason
		if w.Message != "" {
			msg += " — " + shorten(w.Message, 200)
		} else if t := st.LastTerminationState.Terminated; t != nil && t.Message != "" {
			msg += " — " + shorten(t.Message, 200)
		}
		return msg
	}
	return ""
}

// lastWarning is the newest warning Kubernetes recorded for a pod: a probe
// that fails says why in an event, not in the pod.
func (m *Manager) lastWarning(ctx context.Context, namespace, pod string) string {
	cs, err := m.Clientset("")
	if err != nil {
		return ""
	}
	evs, err := cs.CoreV1().Events(namespace).List(ctx, metav1.ListOptions{
		FieldSelector: "involvedObject.name=" + pod + ",type=Warning",
	})
	if err != nil || len(evs.Items) == 0 {
		return ""
	}
	newest := evs.Items[0]
	for _, e := range evs.Items[1:] {
		if eventTime(e).After(eventTime(newest).Time) {
			newest = e
		}
	}
	return strings.Join(strings.Fields(newest.Reason+" — "+shorten(newest.Message, 200)), " ")
}

func eventTime(e corev1.Event) (t metav1.Time) {
	switch {
	case !e.LastTimestamp.IsZero():
		return e.LastTimestamp
	case !e.EventTime.IsZero():
		return metav1.Time{Time: e.EventTime.Time}
	}
	return e.CreationTimestamp
}
