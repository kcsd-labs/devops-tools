package kube

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/kubectl/pkg/describe"
)

// ErrNoController means the pod has no controller — a bare pod or a Job — so
// there is nothing to roll out.
var ErrNoController = errors.New("this pod has no controller (Deployment, StatefulSet or DaemonSet), so it cannot be rolled out")

// PodSummary is a pod as the list view needs it.
type PodSummary struct {
	Name     string `json:"name"`
	Status   string `json:"status"`
	Ready    string `json:"ready"` // "1/1"
	Restarts int32  `json:"restarts"`
	Node     string `json:"node"`
	Age      string `json:"age"` // RFC3339 creationTimestamp; formatted in the UI
	// ImageUnavailable is the one failure the portal can offer to fix from the
	// list itself, so it is reported with the row rather than left to be found
	// by opening each unhealthy pod in turn. Taken from the container's current
	// state, not from the event history — a pod that has recovered does not
	// keep claiming it.
	ImageUnavailable bool `json:"imageUnavailable"`
	// UnavailableImage is the tag that cannot be pulled, and is empty otherwise.
	// Named in the list because whoever reads that a pod cannot pull its image
	// goes looking for exactly this next, and it is already known here.
	UnavailableImage string `json:"unavailableImage,omitempty"`
}

// ListPods returns every pod in the namespace.
func (m *Manager) ListPods(ctx context.Context, asUser, namespace string) ([]PodSummary, error) {
	cs, err := m.Clientset(asUser)
	if err != nil {
		return nil, err
	}
	list, err := cs.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	out := make([]PodSummary, 0, len(list.Items))
	for i := range list.Items {
		out = append(out, summarize(&list.Items[i]))
	}
	return out, nil
}

func summarize(p *corev1.Pod) PodSummary {
	var ready, total, restarts int32
	imageUnavailable := false
	unavailableImage := ""
	for _, cs := range p.Status.ContainerStatuses {
		total++
		if cs.Ready {
			ready++
		}
		restarts += cs.RestartCount
		if w := cs.State.Waiting; w != nil && (w.Reason == "ImagePullBackOff" || w.Reason == "ErrImagePull") {
			imageUnavailable = true
			if unavailableImage == "" {
				unavailableImage = cs.Image
			}
		}
	}
	return PodSummary{
		Name:             p.Name,
		Status:           string(p.Status.Phase),
		Ready:            itoa(ready) + "/" + itoa(total),
		Restarts:         restarts,
		Node:             p.Spec.NodeName,
		Age:              p.CreationTimestamp.UTC().Format("2006-01-02T15:04:05Z"),
		ImageUnavailable: imageUnavailable,
		UnavailableImage: unavailableImage,
	}
}

// PodIssues reports the pod's current container state rather than its event
// history. That distinction matters: a stale "image pull failed" event would
// otherwise keep a banner up long after the image was pulled successfully.
func (m *Manager) PodIssues(ctx context.Context, asUser, namespace, name string) (imageUnavailable, crashLooping bool, err error) {
	cs, err := m.Clientset(asUser)
	if err != nil {
		return false, false, err
	}
	pod, err := cs.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return false, false, err
	}
	for _, st := range pod.Status.ContainerStatuses {
		if w := st.State.Waiting; w != nil {
			switch w.Reason {
			case "ImagePullBackOff", "ErrImagePull":
				imageUnavailable = true
			case "CrashLoopBackOff":
				crashLooping = true
			}
		}
	}
	return imageUnavailable, crashLooping, nil
}

// PodImage returns the image of the container that cannot be pulled, or the
// first container's image when nothing is failing.
func (m *Manager) PodImage(ctx context.Context, asUser, namespace, name string) (string, error) {
	cs, err := m.Clientset(asUser)
	if err != nil {
		return "", err
	}
	pod, err := cs.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	// prefer the container that is actually failing
	for _, cs := range pod.Status.ContainerStatuses {
		if w := cs.State.Waiting; w != nil {
			if w.Reason == "ImagePullBackOff" || w.Reason == "ErrImagePull" {
				if cs.Image != "" {
					return cs.Image, nil
				}
			}
		}
	}
	if len(pod.Spec.Containers) > 0 {
		return pod.Spec.Containers[0].Image, nil
	}
	return "", fmt.Errorf("pod %s has no containers", name)
}

// PodExists tells a deleted pod apart from other failures. It is needed because
// the describer returns only events, without an error, for a pod that is gone.
func (m *Manager) PodExists(ctx context.Context, asUser, namespace, name string) (bool, error) {
	cs, err := m.Clientset(asUser)
	if err != nil {
		return false, err
	}
	_, err = cs.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// DescribePod produces the same text as `kubectl describe pod`: containers with
// their images, limits and environment, the last termination state (OOMKilled
// among them), restart counts, conditions and the event list.
func (m *Manager) DescribePod(asUser, namespace, name string) (string, error) {
	cfg := m.restConfig(asUser)
	describer, ok := describe.DescriberFor(schema.GroupKind{Group: "", Kind: "Pod"}, cfg)
	if !ok {
		return "", fmt.Errorf("no describer for pod")
	}
	return describer.Describe(namespace, name, describe.DescriberSettings{ShowEvents: true})
}

// PodInfo carries what the metrics queries need: the pod IP, used to match JVM
// metrics, and the resource limits to draw against.
type PodInfo struct {
	IP       string  `json:"ip"`
	CPULimit float64 `json:"cpuLimit"` // cores; 0 means no limit is set
	MemLimit int64   `json:"memLimit"` // bytes; 0 means no limit is set
}

// GetPodInfo returns the pod IP and the sum of its containers' limits.
func (m *Manager) GetPodInfo(ctx context.Context, asUser, namespace, name string) (*PodInfo, error) {
	cs, err := m.Clientset(asUser)
	if err != nil {
		return nil, err
	}
	pod, err := cs.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	info := &PodInfo{IP: pod.Status.PodIP}
	for i := range pod.Spec.Containers {
		lim := pod.Spec.Containers[i].Resources.Limits
		if q, ok := lim[corev1.ResourceCPU]; ok {
			info.CPULimit += float64(q.MilliValue()) / 1000
		}
		if q, ok := lim[corev1.ResourceMemory]; ok {
			info.MemLimit += q.Value()
		}
	}
	return info, nil
}

// PodAppLabel returns the service label, which stays the same across restarts
// while the pod name does not. It is what makes it possible to query the logs of
// a whole service, including pods that no longer exist. Empty if unlabelled.
func (m *Manager) PodAppLabel(ctx context.Context, asUser, namespace, name string) (string, error) {
	cs, err := m.Clientset(asUser)
	if err != nil {
		return "", err
	}
	pod, err := cs.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	for _, k := range appLabelKeys {
		if v := pod.Labels[k]; v != "" {
			return v, nil
		}
	}
	return "", nil
}

// appLabelKeys are the labels commonly used to name a service, in priority order.
var appLabelKeys = []string{"app", "app.kubernetes.io/name", "app.kubernetes.io/instance"}

// RunningPodsForApp lists a service's running pods, newest first. Several label
// keys are accepted, since not every chart uses a plain `app`. Used to offer the
// replacement after the pod someone was looking at has been rolled away.
func (m *Manager) RunningPodsForApp(ctx context.Context, asUser, namespace, app string) ([]string, error) {
	cs, err := m.Clientset(asUser)
	if err != nil {
		return nil, err
	}
	list, err := cs.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	running := make([]corev1.Pod, 0, len(list.Items))
	for i := range list.Items {
		p := list.Items[i]
		if p.Status.Phase != corev1.PodRunning || p.DeletionTimestamp != nil {
			continue
		}
		for _, k := range appLabelKeys {
			if p.Labels[k] == app {
				running = append(running, p)
				break
			}
		}
	}
	sort.Slice(running, func(i, j int) bool {
		return running[i].CreationTimestamp.After(running[j].CreationTimestamp.Time)
	})
	names := make([]string, 0, len(running))
	for i := range running {
		names = append(names, running[i].Name)
	}
	return names, nil
}

// PodEvent is one entry of the pod's event list.
type PodEvent struct {
	Type    string `json:"type"`    // Normal | Warning
	Reason  string `json:"reason"`  // Scheduled, Pulling, Failed, BackOff …
	Message string `json:"message"` // the event text
	Count   int32  `json:"count"`   // how many times it repeated
	Last    string `json:"last"`    // RFC3339 of the most recent occurrence
}

// PodEvents returns the pod's events, oldest first — the usual way to find out
// why a pod is Pending or cannot pull its image.
func (m *Manager) PodEvents(ctx context.Context, asUser, namespace, name string) ([]PodEvent, error) {
	cs, err := m.Clientset(asUser)
	if err != nil {
		return nil, err
	}
	list, err := cs.CoreV1().Events(namespace).List(ctx, metav1.ListOptions{
		FieldSelector: "involvedObject.name=" + name + ",involvedObject.kind=Pod",
	})
	if err != nil {
		return nil, err
	}
	out := make([]PodEvent, 0, len(list.Items))
	for i := range list.Items {
		e := &list.Items[i]
		last := e.LastTimestamp.Time
		if last.IsZero() {
			last = e.EventTime.Time // newer event format
		}
		count := e.Count
		if count == 0 {
			count = 1
		}
		out = append(out, PodEvent{
			Type:    e.Type,
			Reason:  e.Reason,
			Message: e.Message,
			Count:   count,
			Last:    last.UTC().Format("2006-01-02T15:04:05Z"),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Last < out[j].Last })
	return out, nil
}

// Containers lists the pod's containers for the log viewer's picker, with the
// default container first.
func (m *Manager) Containers(ctx context.Context, asUser, namespace, name string) ([]string, error) {
	cs, err := m.Clientset(asUser)
	if err != nil {
		return nil, err
	}
	pod, err := cs.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(pod.Spec.Containers))
	for i := range pod.Spec.Containers {
		names = append(names, pod.Spec.Containers[i].Name)
	}
	// move the annotated default container to the front
	if def := pod.Annotations["kubectl.kubernetes.io/default-container"]; def != "" {
		for i, n := range names {
			if n == def && i != 0 {
				names[0], names[i] = names[i], names[0]
				break
			}
		}
	}
	return names, nil
}

// LogOptions are the parameters of a log request.
type LogOptions struct {
	Container string
	Follow    bool
	TailLines *int64
	Previous  bool
}

// PodLogs opens the log stream. The caller must close the returned reader.
func (m *Manager) PodLogs(ctx context.Context, asUser, namespace, name string, opt LogOptions) (io.ReadCloser, error) {
	cs, err := m.Clientset(asUser)
	if err != nil {
		return nil, err
	}
	container := opt.Container
	if container == "" {
		// The API refuses a multi-container pod without an explicit container,
		// so pick the same default kubectl would.
		if c, err := m.defaultContainer(ctx, cs, namespace, name); err == nil {
			container = c
		}
	}
	req := cs.CoreV1().Pods(namespace).GetLogs(name, &corev1.PodLogOptions{
		Container: container,
		Follow:    opt.Follow,
		TailLines: opt.TailLines,
		Previous:  opt.Previous,
	})
	return req.Stream(ctx)
}

// defaultContainer picks what kubectl would: the container named by the
// default-container annotation, otherwise the first one in the spec.
func (m *Manager) defaultContainer(ctx context.Context, cs kubernetes.Interface, namespace, name string) (string, error) {
	pod, err := cs.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	if c := pod.Annotations["kubectl.kubernetes.io/default-container"]; c != "" {
		return c, nil
	}
	if len(pod.Spec.Containers) > 0 {
		return pod.Spec.Containers[0].Name, nil
	}
	return "", fmt.Errorf("pod %s has no containers", name)
}

// WorkloadRef identifies the controller that owns a pod.
type WorkloadRef struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

// ResolveWorkload walks up to the owning controller: a Deployment via its
// ReplicaSet, or a StatefulSet or DaemonSet directly. Returns ok=false for Jobs
// and bare pods, which cannot be rolled out.
func (m *Manager) ResolveWorkload(ctx context.Context, asUser, namespace, pod string) (WorkloadRef, bool, error) {
	cs, err := m.Clientset(asUser)
	if err != nil {
		return WorkloadRef{}, false, err
	}
	p, err := cs.CoreV1().Pods(namespace).Get(ctx, pod, metav1.GetOptions{})
	if err != nil {
		return WorkloadRef{}, false, err
	}
	owner := metav1.GetControllerOf(p)
	if owner == nil {
		return WorkloadRef{}, false, nil
	}
	switch owner.Kind {
	case "ReplicaSet":
		rs, err := cs.AppsV1().ReplicaSets(namespace).Get(ctx, owner.Name, metav1.GetOptions{})
		if err != nil {
			return WorkloadRef{}, false, err
		}
		if d := metav1.GetControllerOf(rs); d != nil && d.Kind == "Deployment" {
			return WorkloadRef{Kind: "Deployment", Name: d.Name}, true, nil
		}
		return WorkloadRef{}, false, nil
	case "StatefulSet":
		return WorkloadRef{Kind: "StatefulSet", Name: owner.Name}, true, nil
	case "DaemonSet":
		return WorkloadRef{Kind: "DaemonSet", Name: owner.Name}, true, nil
	default: // Job, bare pod, or something we do not manage
		return WorkloadRef{}, false, nil
	}
}

const restartAnnotation = "kubectl.kubernetes.io/restartedAt"

// restartDedupeWindow suppresses a repeat rollout of a workload that was just
// restarted. This is what makes selecting several replicas of one service — or
// double-clicking — result in a single rollout rather than several.
const restartDedupeWindow = 10 * time.Second

// RolloutRestart does what `kubectl rollout restart` does: it stamps the pod
// template with a restartedAt annotation, which triggers an ordinary rolling
// update. The old pod keeps serving traffic until the new one passes its probes.
//
// Returns ErrNoController for pods that have no controller, and quietly does
// nothing if the workload was restarted moments ago (see restartDedupeWindow).
func (m *Manager) RolloutRestart(ctx context.Context, asUser, namespace, pod string) (WorkloadRef, error) {
	wl, ok, err := m.ResolveWorkload(ctx, asUser, namespace, pod)
	if err != nil {
		return WorkloadRef{}, err
	}
	if !ok {
		return WorkloadRef{}, ErrNoController
	}
	cs, err := m.Clientset(asUser)
	if err != nil {
		return WorkloadRef{}, err
	}

	// Read the current stamp first: re-stamping would create another revision and
	// restart the rollout from the beginning.
	now := time.Now()
	var last string
	switch wl.Kind {
	case "Deployment":
		d, e := cs.AppsV1().Deployments(namespace).Get(ctx, wl.Name, metav1.GetOptions{})
		if e != nil {
			return wl, e
		}
		last = d.Spec.Template.Annotations[restartAnnotation]
	case "StatefulSet":
		s, e := cs.AppsV1().StatefulSets(namespace).Get(ctx, wl.Name, metav1.GetOptions{})
		if e != nil {
			return wl, e
		}
		last = s.Spec.Template.Annotations[restartAnnotation]
	case "DaemonSet":
		ds, e := cs.AppsV1().DaemonSets(namespace).Get(ctx, wl.Name, metav1.GetOptions{})
		if e != nil {
			return wl, e
		}
		last = ds.Spec.Template.Annotations[restartAnnotation]
	}
	if t, perr := time.Parse(time.RFC3339, last); perr == nil && now.Sub(t) < restartDedupeWindow {
		return wl, nil // restarted moments ago; treat as done rather than rolling again
	}

	patch := []byte(fmt.Sprintf(
		`{"spec":{"template":{"metadata":{"annotations":{%q:%q}}}}}`,
		restartAnnotation, now.Format(time.RFC3339)))
	switch wl.Kind {
	case "Deployment":
		_, err = cs.AppsV1().Deployments(namespace).Patch(ctx, wl.Name, types.StrategicMergePatchType, patch, metav1.PatchOptions{})
	case "StatefulSet":
		_, err = cs.AppsV1().StatefulSets(namespace).Patch(ctx, wl.Name, types.StrategicMergePatchType, patch, metav1.PatchOptions{})
	case "DaemonSet":
		_, err = cs.AppsV1().DaemonSets(namespace).Patch(ctx, wl.Name, types.StrategicMergePatchType, patch, metav1.PatchOptions{})
	}
	return wl, err
}

func itoa(i int32) string {
	// small helper that avoids strconv on the list hot path
	if i == 0 {
		return "0"
	}
	var b [12]byte
	pos := len(b)
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(b[pos:])
}
