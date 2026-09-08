package kube

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
)

// MultiPodLogs merges the logs of several pods into one stream: every line is
// tagged with its pod and the result is ordered by time, the way a label-based
// query would present it.
func (m *Manager) MultiPodLogs(ctx context.Context, asUser, namespace string, pods []string, tail int64) (string, error) {
	cs, err := m.Clientset(asUser)
	if err != nil {
		return "", err
	}

	type entry struct {
		ts   string
		text string
	}
	var all []entry

	for _, pod := range pods {
		// A multi-container pod needs an explicit container, as kubectl requires.
		container, _ := m.defaultContainer(ctx, cs, namespace, pod)
		req := cs.CoreV1().Pods(namespace).GetLogs(pod, &corev1.PodLogOptions{
			Container:  container,
			TailLines:  &tail,
			Timestamps: true, // RFC3339 prefix — this is what we sort on
		})
		stream, err := req.Stream(ctx)
		if err != nil {
			all = append(all, entry{ts: "", text: fmt.Sprintf("[%s] (could not read logs: %v)", pod, err)})
			continue
		}
		data, _ := io.ReadAll(stream)
		stream.Close()

		for _, raw := range strings.Split(string(data), "\n") {
			if raw == "" {
				continue
			}
			ts, msg := splitTimestamp(raw)
			all = append(all, entry{ts: ts, text: fmt.Sprintf("[%s] %s", pod, msg)})
		}
	}

	// RFC3339Nano sorts lexicographically, which is also chronologically.
	sort.SliceStable(all, func(i, j int) bool { return all[i].ts < all[j].ts })

	var b strings.Builder
	for _, e := range all {
		b.WriteString(e.text)
		b.WriteByte('\n')
	}
	return b.String(), nil
}

// splitTimestamp separates the leading timestamp from the message.
func splitTimestamp(line string) (ts, msg string) {
	if i := strings.IndexByte(line, ' '); i > 0 {
		return line[:i], line[i+1:]
	}
	return "", line
}
