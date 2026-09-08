// Package kube owns the connection to the cluster and every operation performed
// against it.
package kube

import (
	"fmt"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"devops-tools/internal/config"
)

// Manager holds the cluster connection and hands out clients. One instance of
// the service serves exactly one cluster: the one it runs in.
// One instance of the service serves exactly one cluster.
type Manager struct {
	config        *rest.Config
	impersonation config.ImpersonationConfig
}

// NewManager connects either in-cluster, using the pod's ServiceAccount, or
// through a kubeconfig file for local development.
func NewManager(cluster config.ClusterConfig, imp config.ImpersonationConfig) (*Manager, error) {
	var (
		restCfg *rest.Config
		err     error
	)
	if cluster.InCluster {
		restCfg, err = rest.InClusterConfig()
	} else {
		restCfg, err = clientcmd.BuildConfigFromFlags("", cluster.Kubeconfig)
	}
	if err != nil {
		return nil, fmt.Errorf("build kube config: %w", err)
	}
	return &Manager{config: restCfg, impersonation: imp}, nil
}

// restConfig returns a copy of the connection, optionally impersonating the
// end user. Impersonation makes the API server's own audit log name the real
// person, and lets Kubernetes RBAC act as a second gate behind ours.
func (m *Manager) restConfig(asUser string) *rest.Config {
	cfg := rest.CopyConfig(m.config)
	if m.impersonation.Enabled && asUser != "" {
		cfg.Impersonate = rest.ImpersonationConfig{
			UserName: asUser,
			Groups:   []string{m.impersonation.DefaultGroup},
		}
	}
	return cfg
}

// Clientset returns a typed Kubernetes client.
func (m *Manager) Clientset(asUser string) (*kubernetes.Clientset, error) {
	return kubernetes.NewForConfig(m.restConfig(asUser))
}
