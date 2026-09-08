package kube

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// RolloutStatus reports how far a rollout has progressed — the same question
// kubectl rollout status answers — and, when it is not progressing, why the new
// pod is not becoming ready. The UI turns that into a plain-language
// explanation instead of an endless spinner.
type RolloutStatus struct {
	Kind             string `json:"kind"`
	Name             string `json:"name"`
	Desired          int32  `json:"desired"`
	Updated          int32  `json:"updated"`
	Ready            int32  `json:"ready"`
	Available        int32  `json:"available"`
	Done             bool   `json:"done"`
	DeadlineExceeded bool   `json:"deadlineExceeded"`
	// Reason is why the rollout is stuck: image, crash, config, unschedulable,
	// or empty. It is taken from pods that are neither ready nor being deleted,
	// so the healthy old pod is never blamed and the failing new one is.
	Reason    string `json:"reason"`
	ReasonPod string `json:"reasonPod"`
}

// RolloutStatus reports the progress of a Deployment, StatefulSet or DaemonSet.
func (m *Manager) RolloutStatus(ctx context.Context, asUser, namespace, kind, name string) (RolloutStatus, error) {
	cs, err := m.Clientset(asUser)
	if err != nil {
		return RolloutStatus{}, err
	}
	st := RolloutStatus{Kind: kind, Name: name}
	var selector *metav1.LabelSelector

	switch kind {
	case "Deployment":
		d, err := cs.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return st, err
		}
		desired := int32(1)
		if d.Spec.Replicas != nil {
			desired = *d.Spec.Replicas
		}
		st.Desired, st.Updated = desired, d.Status.UpdatedReplicas
		st.Ready, st.Available = d.Status.ReadyReplicas, d.Status.AvailableReplicas
		// Same conditions kubectl uses: everything updated, nothing old left,
		// and all replicas available.
		st.Done = d.Status.ObservedGeneration >= d.Generation &&
			d.Status.UpdatedReplicas == desired &&
			d.Status.Replicas == d.Status.UpdatedReplicas &&
			d.Status.AvailableReplicas == d.Status.UpdatedReplicas
		for _, c := range d.Status.Conditions {
			if c.Type == appsv1.DeploymentProgressing && c.Status == corev1.ConditionFalse &&
				c.Reason == "ProgressDeadlineExceeded" {
				st.DeadlineExceeded = true
			}
		}
		selector = d.Spec.Selector

	case "StatefulSet":
		s, err := cs.AppsV1().StatefulSets(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return st, err
		}
		desired := int32(1)
		if s.Spec.Replicas != nil {
			desired = *s.Spec.Replicas
		}
		st.Desired, st.Updated = desired, s.Status.UpdatedReplicas
		st.Ready, st.Available = s.Status.ReadyReplicas, s.Status.AvailableReplicas
		st.Done = s.Status.ObservedGeneration >= s.Generation &&
			s.Status.UpdatedReplicas == desired &&
			s.Status.ReadyReplicas == desired &&
			s.Status.CurrentRevision == s.Status.UpdateRevision
		selector = s.Spec.Selector

	case "DaemonSet":
		ds, err := cs.AppsV1().DaemonSets(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return st, err
		}
		st.Desired, st.Updated = ds.Status.DesiredNumberScheduled, ds.Status.UpdatedNumberScheduled
		st.Ready, st.Available = ds.Status.NumberReady, ds.Status.NumberAvailable
		st.Done = ds.Status.ObservedGeneration >= ds.Generation &&
			ds.Status.UpdatedNumberScheduled == ds.Status.DesiredNumberScheduled &&
			ds.Status.NumberReady == ds.Status.DesiredNumberScheduled
		selector = ds.Spec.Selector

	default:
		return st, fmt.Errorf("unsupported workload kind %q", kind)
	}

	if st.Done {
		return st, nil
	}

	// Not finished: work out why, from the pods that are not ready.
	sel, err := metav1.LabelSelectorAsSelector(selector)
	if err != nil {
		return st, nil // no diagnosis, but the progress is still useful
	}
	pods, err := cs.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: sel.String()})
	if err != nil {
		return st, nil
	}
	rank := map[string]int{"image": 4, "crash": 3, "config": 2, "unschedulable": 1}
	for i := range pods.Items {
		p := &pods.Items[i]
		if p.DeletionTimestamp != nil || podReady(p) {
			continue // terminating or already-ready pods are not the problem
		}
		if r := podStuckReason(p); rank[r] > rank[st.Reason] {
			st.Reason, st.ReasonPod = r, p.Name
		}
	}
	return st, nil
}

func podReady(p *corev1.Pod) bool {
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

// podStuckReason recognises the failure modes worth explaining to a user.
func podStuckReason(p *corev1.Pod) string {
	for _, st := range p.Status.ContainerStatuses {
		if w := st.State.Waiting; w != nil {
			switch w.Reason {
			case "ImagePullBackOff", "ErrImagePull":
				return "image"
			case "CrashLoopBackOff":
				return "crash"
			case "CreateContainerConfigError":
				return "config"
			}
		}
	}
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodScheduled && c.Status == corev1.ConditionFalse &&
			c.Reason == corev1.PodReasonUnschedulable {
			return "unschedulable"
		}
	}
	return ""
}
