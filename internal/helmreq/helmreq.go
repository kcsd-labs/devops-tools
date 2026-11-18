// Package helmreq keeps requests to install or upgrade a chart from the
// catalog until an administrator answers them.
//
// Each request is a Secret in the portal's own namespace: the values in one
// may hold a password, several replicas must see the same requests, and a
// request is small and short-lived — nothing about it belongs in the access
// model. Outside a cluster they are kept in memory.
package helmreq

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// Kinds of request.
const (
	KindInstall = "install"
	KindUpgrade = "upgrade"
)

// Statuses. Waiting is the only one an answer changes; installing becomes
// installed or failed on its own.
const (
	StatusWaiting    = "waiting"
	StatusInstalling = "installing"
	StatusInstalled  = "installed"
	StatusFailed     = "failed"
	StatusRejected   = "rejected"
	StatusWithdrawn  = "withdrawn"
	StatusExpired    = "expired"
)

// Request is one ask to install or upgrade.
type Request struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	Namespace   string `json:"namespace"`
	Release     string `json:"release"`
	Repo        string `json:"repo"`
	Chart       string `json:"chart"`
	Version     string `json:"version"`
	FromVersion string `json:"fromVersion,omitempty"`
	// Digest is the archive the request was checked against.
	Digest string `json:"digest"`
	// Values are what will be installed.
	Values string `json:"values"`
	// AskedValues are what the requester sent, kept when an administrator
	// changed them before approving, so the requester can see what changed.
	AskedValues *string `json:"askedValues,omitempty"`
	// Objects is what the plan said would be created, for the reviewer.
	Objects []string `json:"objects,omitempty"`
	// NewNamespace: the namespace does not exist yet, and is created with
	// the release. GrantedRoles are the requester's roles given access to it
	// on approval.
	NewNamespace bool     `json:"newNamespace,omitempty"`
	GrantedRoles []string `json:"grantedRoles,omitempty"`
	// Notes are what the chart says comes after the install: its NOTES.txt.
	Notes string `json:"notes,omitempty"`
	// Help is what the requester could not fill in and asks an
	// administrator to: the values need not pass the check yet.
	Help string `json:"help,omitempty"`

	Requester string     `json:"requester"`
	Created   time.Time  `json:"created"`
	Expires   time.Time  `json:"expires"`
	Status    string     `json:"status"`
	Decider   string     `json:"decider,omitempty"`
	Decided   *time.Time `json:"decided,omitempty"`
	Comment   string     `json:"comment,omitempty"`
	// Detail is how an install ended: what failed, or nothing.
	Detail string `json:"detail,omitempty"`
}

// Open reports whether the request still waits for an answer.
func (r *Request) Open() bool { return r.Status == StatusWaiting }

// ErrNotFound says there is no such request.
var ErrNotFound = errors.New("no such request")

// Store keeps requests.
type Store interface {
	Create(ctx context.Context, r *Request) error
	Get(ctx context.Context, id string) (*Request, error)
	// List returns every request, newest first.
	List(ctx context.Context) ([]*Request, error)
	// Update changes one request. change may be called more than once if
	// somebody else wrote in between, and must not have other effects.
	Update(ctx context.Context, id string, change func(*Request) error) (*Request, error)
}

// keepFinished is how long an answered request stays visible.
const keepFinished = 90 * 24 * time.Hour

// NewID returns a new request id.
func NewID() string {
	b := make([]byte, 5)
	_, _ = rand.Read(b)
	return time.Now().UTC().Format("060102") + hex.EncodeToString(b)
}

// expire marks a request whose time ran out. It reports whether it changed.
func expire(r *Request, now time.Time) bool {
	if r.Status == StatusWaiting && now.After(r.Expires) {
		r.Status = StatusExpired
		return true
	}
	return false
}

func stale(r *Request, now time.Time) bool {
	if r.Status == StatusWaiting || r.Status == StatusInstalling {
		return false
	}
	end := r.Created
	if r.Decided != nil {
		end = *r.Decided
	}
	return now.Sub(end) > keepFinished
}

func newestFirst(rs []*Request) {
	sort.Slice(rs, func(i, j int) bool { return rs[i].Created.After(rs[j].Created) })
}

// ---- in memory ----

type memory struct {
	mu   sync.Mutex
	reqs map[string]*Request
	now  func() time.Time
}

// NewMemory keeps requests in memory, for running outside a cluster.
func NewMemory() Store { return &memory{reqs: map[string]*Request{}, now: time.Now} }

func clone(r *Request) *Request {
	b, _ := json.Marshal(r)
	var out Request
	_ = json.Unmarshal(b, &out)
	return &out
}

func (m *memory) Create(_ context.Context, r *Request) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r.ID == "" {
		r.ID = NewID()
	}
	m.reqs[r.ID] = clone(r)
	return nil
}

func (m *memory) Get(_ context.Context, id string) (*Request, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.reqs[id]
	if !ok {
		return nil, ErrNotFound
	}
	expire(r, m.now())
	return clone(r), nil
}

func (m *memory) List(_ context.Context) ([]*Request, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	out := []*Request{}
	for id, r := range m.reqs {
		expire(r, now)
		if stale(r, now) {
			delete(m.reqs, id)
			continue
		}
		out = append(out, clone(r))
	}
	newestFirst(out)
	return out, nil
}

func (m *memory) Update(_ context.Context, id string, change func(*Request) error) (*Request, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.reqs[id]
	if !ok {
		return nil, ErrNotFound
	}
	expire(r, m.now())
	c := clone(r)
	if err := change(c); err != nil {
		return nil, err
	}
	m.reqs[id] = c
	return clone(c), nil
}

// ---- Secrets ----

const (
	labelRequest = "devops-tools-helm-request"
	secretPrefix = "devops-tools-helm-request-"
	dataKey      = "request.json"
)

type secrets struct {
	client    kubernetes.Interface
	namespace string
	now       func() time.Time
}

// NewSecrets keeps requests as Secrets in namespace.
func NewSecrets(c kubernetes.Interface, namespace string) Store {
	return &secrets{client: c, namespace: namespace, now: time.Now}
}

func (s *secrets) Create(ctx context.Context, r *Request) error {
	if r.ID == "" {
		r.ID = NewID()
	}
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	sec := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretPrefix + r.ID,
			Namespace: s.namespace,
			Labels: map[string]string{
				labelRequest:                   "true",
				"app.kubernetes.io/managed-by": "devops-tools",
			},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{dataKey: data},
	}
	if _, err := s.client.CoreV1().Secrets(s.namespace).Create(ctx, sec, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("save the request: %w", err)
	}
	return nil
}

func decode(sec *corev1.Secret) (*Request, error) {
	var r Request
	if err := json.Unmarshal(sec.Data[dataKey], &r); err != nil {
		return nil, fmt.Errorf("request %s does not read: %w", sec.Name, err)
	}
	return &r, nil
}

func (s *secrets) Get(ctx context.Context, id string) (*Request, error) {
	sec, err := s.client.CoreV1().Secrets(s.namespace).Get(ctx, secretPrefix+id, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	r, err := decode(sec)
	if err != nil {
		return nil, err
	}
	if expire(r, s.now()) {
		_ = s.write(ctx, sec, r)
	}
	return r, nil
}

func (s *secrets) List(ctx context.Context) ([]*Request, error) {
	list, err := s.client.CoreV1().Secrets(s.namespace).List(ctx, metav1.ListOptions{LabelSelector: labelRequest + "=true"})
	if err != nil {
		return nil, fmt.Errorf("read the requests: %w", err)
	}
	now := s.now()
	out := []*Request{}
	for i := range list.Items {
		sec := &list.Items[i]
		r, err := decode(sec)
		if err != nil {
			continue
		}
		if stale(r, now) {
			_ = s.client.CoreV1().Secrets(s.namespace).Delete(ctx, sec.Name, metav1.DeleteOptions{})
			continue
		}
		if expire(r, now) {
			_ = s.write(ctx, sec, r)
		}
		out = append(out, r)
	}
	newestFirst(out)
	return out, nil
}

func (s *secrets) Update(ctx context.Context, id string, change func(*Request) error) (*Request, error) {
	for attempt := 0; attempt < 5; attempt++ {
		sec, err := s.client.CoreV1().Secrets(s.namespace).Get(ctx, secretPrefix+id, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return nil, ErrNotFound
		}
		if err != nil {
			return nil, err
		}
		r, err := decode(sec)
		if err != nil {
			return nil, err
		}
		expire(r, s.now())
		if err := change(r); err != nil {
			return nil, err
		}
		err = s.write(ctx, sec, r)
		if apierrors.IsConflict(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		return r, nil
	}
	return nil, errors.New("the request kept changing under this update; try again")
}

// write saves r into sec, at the version sec was read at.
func (s *secrets) write(ctx context.Context, sec *corev1.Secret, r *Request) error {
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	sec = sec.DeepCopy()
	sec.Data = map[string][]byte{dataKey: data}
	_, err = s.client.CoreV1().Secrets(s.namespace).Update(ctx, sec, metav1.UpdateOptions{})
	return err
}
