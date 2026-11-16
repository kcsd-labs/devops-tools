package helmreq

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// Template is what an administrator wrote once for a chart — the company's
// domain, its registry, the email a certificate needs — and everyone who
// installs that chart starts from. Kept beside the requests, as a Secret: it
// may hold what a chart calls a password.
type Template struct {
	Repo    string    `json:"repo"`
	Chart   string    `json:"chart"`
	Values  string    `json:"values"`
	Note    string    `json:"note,omitempty"`
	Author  string    `json:"author"`
	Updated time.Time `json:"updated"`
}

// Templates keeps one template per chart.
type Templates interface {
	// Get returns the chart's template, or nil when it has none.
	Get(ctx context.Context, repo, chart string) (*Template, error)
	Put(ctx context.Context, t *Template) error
	Delete(ctx context.Context, repo, chart string) error
}

type memoryTemplates struct {
	mu sync.Mutex
	m  map[string]Template
}

// NewMemoryTemplates keeps templates in memory, for running outside a cluster.
func NewMemoryTemplates() Templates { return &memoryTemplates{m: map[string]Template{}} }

func (m *memoryTemplates) Get(_ context.Context, repo, chart string) (*Template, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.m[repo+"/"+chart]
	if !ok {
		return nil, nil
	}
	return &t, nil
}

func (m *memoryTemplates) Put(_ context.Context, t *Template) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.m[t.Repo+"/"+t.Chart] = *t
	return nil
}

func (m *memoryTemplates) Delete(_ context.Context, repo, chart string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.m, repo+"/"+chart)
	return nil
}

const (
	labelTemplate  = "devops-tools-helm-template"
	templatePrefix = "devops-tools-helm-template-"
	templateKey    = "template.json"
)

type secretTemplates struct {
	client    kubernetes.Interface
	namespace string
}

// NewSecretTemplates keeps templates as Secrets in namespace.
func NewSecretTemplates(c kubernetes.Interface, namespace string) Templates {
	return &secretTemplates{client: c, namespace: namespace}
}

// templateName is the Secret for a chart: repository and chart names may be
// longer together than a name may be, so they are hashed.
func templateName(repo, chart string) string {
	sum := sha256.Sum256([]byte(repo + "/" + chart))
	return templatePrefix + hex.EncodeToString(sum[:10])
}

func (s *secretTemplates) Get(ctx context.Context, repo, chart string) (*Template, error) {
	sec, err := s.client.CoreV1().Secrets(s.namespace).Get(ctx, templateName(repo, chart), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var t Template
	if err := json.Unmarshal(sec.Data[templateKey], &t); err != nil {
		return nil, fmt.Errorf("the starting values for %s/%s do not read: %w", repo, chart, err)
	}
	return &t, nil
}

func (s *secretTemplates) Put(ctx context.Context, t *Template) error {
	data, err := json.Marshal(t)
	if err != nil {
		return err
	}
	name := templateName(t.Repo, t.Chart)
	secrets := s.client.CoreV1().Secrets(s.namespace)
	sec, err := secrets.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = secrets.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: s.namespace,
				Labels: map[string]string{
					labelTemplate:                  "true",
					"app.kubernetes.io/managed-by": "devops-tools",
				},
			},
			Type: corev1.SecretTypeOpaque,
			Data: map[string][]byte{templateKey: data},
		}, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	sec.Data = map[string][]byte{templateKey: data}
	_, err = secrets.Update(ctx, sec, metav1.UpdateOptions{})
	return err
}

func (s *secretTemplates) Delete(ctx context.Context, repo, chart string) error {
	err := s.client.CoreV1().Secrets(s.namespace).Delete(ctx, templateName(repo, chart), metav1.DeleteOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}
