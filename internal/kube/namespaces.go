package kube

import (
	"context"
	"sort"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"devops-tools/internal/config"
)

// ListNamespaces returns the namespaces of the cluster, minus the ones hidden
// by configuration, sorted by name.
//
// It exists for roles granted "*". Those cannot be enumerated from the access
// model itself — a wildcard names no namespaces — so without this the only
// thing the UI could offer such a role was a text box to type a name into.
//
// Terminating namespaces are skipped: they still appear in the API for a while
// after deletion, and opening one shows nothing but confusion.
func (m *Manager) ListNamespaces(ctx context.Context, asUser string, hidden []string) ([]string, error) {
	cs, err := m.Clientset(asUser)
	if err != nil {
		return nil, err
	}
	list, err := cs.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(list.Items))
	for i := range list.Items {
		ns := &list.Items[i]
		if ns.Status.Phase == corev1.NamespaceTerminating {
			continue
		}
		if config.MatchesNamespace(ns.Name, hidden) {
			continue
		}
		out = append(out, ns.Name)
	}
	sort.Strings(out)
	return out, nil
}
