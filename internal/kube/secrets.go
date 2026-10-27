package kube

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"devops-tools/internal/changes"
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
//
// It returns which keys were added, changed and removed, for the audit record.
// Names only — see package changes. The old values are read here anyway, to be
// replaced, so the comparison costs nothing.
func (m *Manager) UpdateSecret(ctx context.Context, asUser, namespace, name string, data map[string]string) (string, error) {
	cs, err := m.Clientset(asUser)
	if err != nil {
		return "", err
	}
	s, err := cs.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	before := make(map[string]string, len(s.Data))
	for k, v := range s.Data {
		before[k] = string(v)
	}
	s.Data = nil        // drop the previous values
	s.StringData = data // and set the new ones
	if _, err := cs.CoreV1().Secrets(namespace).Update(ctx, s, metav1.UpdateOptions{}); err != nil {
		return "", err
	}
	return changes.Keys(before, data), nil
}

// DeleteSecret removes a secret, and returns which keys it held.
//
// "What was in it" is the first question asked about a deletion afterwards, and
// the one nothing can answer once it has happened. The keys are read on a best
// effort basis: somebody allowed to delete a secret may not be allowed to read
// it, and that must not stop the deletion.
func (m *Manager) DeleteSecret(ctx context.Context, asUser, namespace, name string) (string, error) {
	cs, err := m.Clientset(asUser)
	if err != nil {
		return "", err
	}
	var detail string
	if s, gerr := cs.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{}); gerr == nil {
		// Names only; the values are not needed to say what was removed.
		held := make(map[string]string, len(s.Data))
		for k := range s.Data {
			held[k] = ""
		}
		detail = changes.Keys(held, nil)
	}
	if err := cs.CoreV1().Secrets(namespace).Delete(ctx, name, metav1.DeleteOptions{}); err != nil {
		return "", err
	}
	return detail, nil
}
