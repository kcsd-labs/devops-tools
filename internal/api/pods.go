package api

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"devops-tools/internal/audit"
	"devops-tools/internal/auth"
	"devops-tools/internal/kube"
	"devops-tools/internal/prom"
)

func (s *Server) handleListPods(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.FromContext(r.Context())
	namespace := chi.URLParam(r, "namespace")

	pods, err := s.kube.ListPods(r.Context(), user.Username, namespace)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"pods": pods,
		// Alongside the list rather than per pod: the permission is per
		// namespace, and the page shows one namespace. Sent so the row can
		// offer the fix only where pressing it would work — which is the
		// difference between an action and a trap.
		"canRebuildImage": s.gitlab != nil && s.authz.Allowed(user.Roles, namespace, OpImageRebuild),
	})
}

// handlePodContainers returns the pod's containers for the log viewer's
// container picker, plus the service label. The label is captured while the pod
// is alive so that, once it is replaced, we can still find its successor and
// query historical logs for the service.
func (s *Server) handlePodContainers(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.FromContext(r.Context())
	namespace := chi.URLParam(r, "namespace")
	pod := chi.URLParam(r, "pod")

	names, err := s.kube.Containers(r.Context(), user.Username, namespace, pod)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	app, _ := s.kube.PodAppLabel(r.Context(), user.Username, namespace, pod)
	writeJSON(w, http.StatusOK, map[string]any{"containers": names, "app": app})
}

// handleWorkloadPods lists the running pods of a service, newest first. Used to
// offer "open the new pod" after the one being viewed was replaced.
func (s *Server) handleWorkloadPods(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.FromContext(r.Context())
	namespace := chi.URLParam(r, "namespace")
	app := r.URL.Query().Get("app")
	if app == "" {
		http.Error(w, "app is required", http.StatusBadRequest)
		return
	}
	names, err := s.kube.RunningPodsForApp(r.Context(), user.Username, namespace, app)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, names)
}

// handlePodEvents returns the pod's events plus flags describing its current
// state. The flags come from the live container statuses rather than the event
// history, so a stale "image pull failed" event does not keep a banner up after
// the image has been pulled successfully.
func (s *Server) handlePodEvents(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.FromContext(r.Context())
	namespace := chi.URLParam(r, "namespace")
	pod := chi.URLParam(r, "pod")

	events, err := s.kube.PodEvents(r.Context(), user.Username, namespace, pod)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	imageUnavailable, crashLooping, _ := s.kube.PodIssues(r.Context(), user.Username, namespace, pod)
	writeJSON(w, http.StatusOK, map[string]any{
		"events":           events,
		"imageUnavailable": imageUnavailable,
		"crashLooping":     crashLooping,
		// Whether this person, in this namespace, may do anything about it.
		// Answered here rather than left to the button because permission is
		// per namespace, and a button that fails on being pressed is worse than
		// one that is not there — least of all while somebody is looking at a
		// pod that will not start.
		"canRebuildImage": s.gitlab != nil && s.authz.Allowed(user.Roles, namespace, OpImageRebuild),
		"canDeploy":       s.gitlab != nil && s.authz.Allowed(user.Roles, namespace, OpPipelineDeploy),
	})
}

func (s *Server) handlePodLogs(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.FromContext(r.Context())
	namespace := chi.URLParam(r, "namespace")
	pod := chi.URLParam(r, "pod")

	opt := kube.LogOptions{
		Container: r.URL.Query().Get("container"),
		Follow:    r.URL.Query().Get("follow") == "true",
		Previous:  r.URL.Query().Get("previous") == "true",
	}
	if t := r.URL.Query().Get("tail"); t != "" {
		if n, err := strconv.ParseInt(t, 10, 64); err == nil {
			opt.TailLines = &n
		}
	} else {
		def := int64(500)
		opt.TailLines = &def
	}

	stream, err := s.kube.PodLogs(r.Context(), user.Username, namespace, pod, opt)
	if err != nil {
		if apierrors.IsNotFound(err) { // the pod was replaced, e.g. by a deployment
			http.Error(w, "pod not found (it was probably replaced)", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer stream.Close()

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	// Disable proxy buffering, otherwise a followed stream is held in the
	// buffer and the client sees nothing.
	w.Header().Set("X-Accel-Buffering", "no")

	flusher, _ := w.(http.Flusher)
	// Send headers immediately rather than waiting for the first log line —
	// otherwise a follow request appears to hang until something is logged.
	w.WriteHeader(http.StatusOK)
	if flusher != nil {
		flusher.Flush()
	}

	buf := make([]byte, 2048)
	for {
		n, readErr := stream.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if readErr != nil {
			return
		}
	}
}

func (s *Server) handleDescribePod(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.FromContext(r.Context())
	namespace := chi.URLParam(r, "namespace")
	pod := chi.URLParam(r, "pod")

	// The describer silently returns only events for a pod that no longer
	// exists, so existence is checked explicitly.
	if exists, err := s.kube.PodExists(r.Context(), user.Username, namespace, pod); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	} else if !exists {
		http.Error(w, "pod not found (it was probably replaced)", http.StatusNotFound)
		return
	}

	desc, err := s.kube.DescribePod(user.Username, namespace, pod)
	if err != nil {
		if apierrors.IsNotFound(err) {
			http.Error(w, "pod not found (it was probably replaced)", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte(desc))
}

// handleRestartPod performs a rolling restart of the workload owning the pod,
// equivalent to `kubectl rollout restart`. The old pod keeps serving traffic
// until the new one is ready, so there is no downtime — and if the new pod
// fails to start, the old one simply stays.
func (s *Server) handleRestartPod(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.FromContext(r.Context())
	namespace := chi.URLParam(r, "namespace")
	pod := chi.URLParam(r, "pod")

	wl, err := s.kube.RolloutRestart(r.Context(), user.Username, namespace, pod)
	s.audit.Log(audit.Entry{
		User: user.Username, Environment: s.cfg.Environment, Namespace: namespace,
		Operation: OpPodRestart, Target: pod, Allowed: true,
		Success: err == nil, Error: errStr(err),
	})
	if errors.Is(err, kube.ErrNoController) {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	if err != nil {
		if apierrors.IsNotFound(err) {
			http.Error(w, "pod not found (it was probably replaced)", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "restarted", "kind": wl.Kind, "name": wl.Name})
}

// handleRolloutStatus reports how far a rolling restart has progressed and, if
// it is stuck, why — so the UI can explain the situation instead of spinning.
func (s *Server) handleRolloutStatus(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.FromContext(r.Context())
	namespace := chi.URLParam(r, "namespace")
	kind := r.URL.Query().Get("kind")
	name := r.URL.Query().Get("name")
	if kind == "" || name == "" {
		http.Error(w, "kind and name are required", http.StatusBadRequest)
		return
	}
	st, err := s.kube.RolloutStatus(r.Context(), user.Username, namespace, kind, name)
	if err != nil {
		if apierrors.IsNotFound(err) {
			http.Error(w, "workload not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// rangeStep maps a UI range selector to a duration and a sampling step.
//
// The step grows with the range so the number of points stays roughly constant
// — around 120. A fixed step would make the longest range slow to fetch and
// too dense to read, and the shortest one a handful of dots.
//
// An unknown value falls back to an hour rather than failing: the selector is
// the only thing that sends these, and a chart is better than an error.
func rangeStep(s string) (time.Duration, time.Duration) {
	switch s {
	case "15m":
		return 15 * time.Minute, 15 * time.Second
	case "30m":
		return 30 * time.Minute, 30 * time.Second
	case "1h30m":
		return 90 * time.Minute, 45 * time.Second
	default: // 1h
		return time.Hour, 30 * time.Second
	}
}

type metricsResponse struct {
	CPU struct {
		Usage []prom.Point `json:"usage"`
		Limit float64      `json:"limit"`
	} `json:"cpu"`
	Memory struct {
		Usage []prom.Point `json:"usage"`
		Limit int64        `json:"limit"`
	} `json:"memory"`
	JVM *jvmMetrics `json:"jvm,omitempty"`
}

type jvmMetrics struct {
	Heap    []prom.Point `json:"heap"`
	NonHeap []prom.Point `json:"nonheap"`
	GC      []prom.Point `json:"gc"`
}

// handlePodMetrics returns CPU and memory usage for a pod, plus JVM metrics
// when the workload exposes them.
func (s *Server) handlePodMetrics(w http.ResponseWriter, r *http.Request) {
	if s.prom == nil {
		http.Error(w, "metrics are disabled: Prometheus is not configured", http.StatusServiceUnavailable)
		return
	}
	user, _ := auth.FromContext(r.Context())
	ns := chi.URLParam(r, "namespace")
	pod := chi.URLParam(r, "pod")

	info, err := s.kube.GetPodInfo(r.Context(), user.Username, ns, pod)
	if err != nil {
		if apierrors.IsNotFound(err) {
			http.Error(w, "pod not found (it was probably replaced)", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	dur, step := rangeStep(r.URL.Query().Get("range"))
	end := time.Now()
	start := end.Add(-dur)
	ctx := r.Context()

	var resp metricsResponse
	resp.CPU.Limit = info.CPULimit
	resp.Memory.Limit = info.MemLimit

	cpuQ := fmt.Sprintf(`sum(rate(container_cpu_usage_seconds_total{namespace=%q,pod=%q,container!=""}[2m]))`, ns, pod)
	memQ := fmt.Sprintf(`sum(container_memory_working_set_bytes{namespace=%q,pod=%q,container!=""})`, ns, pod)
	resp.CPU.Usage, _ = s.prom.QueryRange(ctx, cpuQ, start, end, step)
	resp.Memory.Usage, _ = s.prom.QueryRange(ctx, memQ, start, end, step)

	// JVM metrics are only fetched when the pod has an IP to match on. The dot
	// in the IP is not escaped: inside a PromQL string literal "\." breaks the
	// literal, and "." already matches a dot in a regex — false matches on a
	// specific IP are not a practical concern.
	if info.IP != "" {
		sel := fmt.Sprintf(`namespace=%q,instance=~"%s:.*"`, ns, info.IP)
		heap, _ := s.prom.QueryRange(ctx, fmt.Sprintf(`sum(jvm_memory_used_bytes{%s,area="heap"})`, sel), start, end, step)
		if len(heap) > 0 {
			nonheap, _ := s.prom.QueryRange(ctx, fmt.Sprintf(`sum(jvm_memory_used_bytes{%s,area="nonheap"})`, sel), start, end, step)
			gc, _ := s.prom.QueryRange(ctx, fmt.Sprintf(`sum(rate(jvm_gc_pause_seconds_sum{%s}[2m]))`, sel), start, end, step)
			resp.JVM = &jvmMetrics{Heap: heap, NonHeap: nonheap, GC: gc}
		}
	}

	writeJSON(w, http.StatusOK, resp)
}
