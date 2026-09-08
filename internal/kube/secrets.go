package kube

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// SecretSummary describes a secret without revealing anything: name, type and
// the key names only.
type SecretSummary struct {
	Name string   `json:"name"`
	Type string   `json:"type"`
	Keys []string `json:"keys"`
}

// SecretData carries the decoded values, for viewing and editing.
type SecretData struct {
	Name string            `json:"name"`
	Type string            `json:"type"`
	Data map[string]string `json:"data"`
}

// ListSecrets returns the secrets of a namespace without their values.
//
// When includeSystem is false, Helm release data and service account tokens are
// excluded with a field selector, so they are never transferred at all — a
// namespace can hold hundreds of Helm release secrets and they are large.
func (m *Manager) ListSecrets(ctx context.Context, asUser, namespace string, includeSystem bool) ([]SecretSummary, error) {
	cs, err := m.Clientset(asUser)
	if err != nil {
		return nil, err
	}
	opts := metav1.ListOptions{}
	if !includeSystem {
		opts.FieldSelector = "type!=helm.sh/release.v1,type!=kubernetes.io/service-account-token"
	}
	list, err := cs.CoreV1().Secrets(namespace).List(ctx, opts)
	if err != nil {
		return nil, err
	}
	out := make([]SecretSummary, 0, len(list.Items))
	for i := range list.Items {
		s := &list.Items[i]
		keys := make([]string, 0, len(s.Data))
		for k := range s.Data {
			keys = append(keys, k)
		}
		out = append(out, SecretSummary{Name: s.Name, Type: string(s.Type), Keys: keys})
	}
	return out, nil
}

// GetSecret returns a secret with its values decoded.
func (m *Manager) GetSecret(ctx context.Context, asUser, namespace, name string) (*SecretData, error) {
	cs, err := m.Clientset(asUser)
	if err != nil {
		return nil, err
	}
	s, err := cs.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	data := make(map[string]string, len(s.Data))
	for k, v := range s.Data { // client-go has already decoded base64
		data[k] = string(v)
	}
	return &SecretData{Name: s.Name, Type: string(s.Type), Data: data}, nil
}

// CreateSecret creates a secret, defaulting to type Opaque. Values are passed
// in plain text and encoded by the API server.
func (m *Manager) CreateSecret(ctx context.Context, asUser, namespace, name, secretType string, data map[string]string) error {
	cs, err := m.Clientset(asUser)
	if err != nil {
		return err
	}
	t := corev1.SecretType(secretType)
	if t == "" {
		t = corev1.SecretTypeOpaque
	}
	s := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Type:       t,
		StringData: data, // the API server encodes this
	}
	_, err = cs.CoreV1().Secrets(namespace).Create(ctx, s, metav1.CreateOptions{})
	return err
}

// UpdateSecret replaces the whole content of a secret: keys absent from data
// are removed, not kept.
func (m *Manager) UpdateSecret(ctx context.Context, asUser, namespace, name string, data map[string]string) error {
	cs, err := m.Clientset(asUser)
	if err != nil {
		return err
	}
	s, err := cs.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	s.Data = nil        // drop the previous values
	s.StringData = data // and set the new ones
	_, err = cs.CoreV1().Secrets(namespace).Update(ctx, s, metav1.UpdateOptions{})
	return err
}

// DeleteSecret removes a secret.
func (m *Manager) DeleteSecret(ctx context.Context, asUser, namespace, name string) error {
	cs, err := m.Clientset(asUser)
	if err != nil {
		return err
	}
	return cs.CoreV1().Secrets(namespace).Delete(ctx, name, metav1.DeleteOptions{})
}
