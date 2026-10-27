package kube

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// Saved requests live beside the service they call: one ConfigMap per
// service, in the service's own namespace, one key per request.
//
// Not in git, and not in the portal's own store. Beside the service, the
// people who may see and call it are exactly the people who may see its
// requests, with no second permission model to keep in step; the collection
// goes when the namespace goes; and it works on installations without
// configuration management. Moving a collection to another environment is an
// export and an import.
const (
	requestsPrefix = "devops-tools-requests-"
	// requestsFor names the service a collection belongs to, so a listing of
	// ConfigMaps says what each of these is.
	requestsFor = "devops-tools/requests-for"
	managedBy   = "app.kubernetes.io/managed-by"
	managedHere = "devops-tools"
	// A ConfigMap holds at most 1 MiB. Some room is left for the object
	// around the data.
	maxCollectionBytes = 900 << 10
)

var (
	ErrCollectionChanged  = errors.New("someone saved this collection since you opened it — reload to see their changes, then save again")
	ErrCollectionReadOnly = errors.New("this collection is managed outside the portal, so it cannot be changed here")
	ErrCollectionFull     = errors.New("the collection would be larger than a ConfigMap can hold")
	// ErrInvalidRequest wraps whatever is wrong with a request being saved.
	ErrInvalidRequest = errors.New("cannot save the request")
)

// Header is one request or response header.
type Header struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// SavedRequest is one request in a collection.
//
// Secrets are never in it. A header or body names a secret instead —
// {{secret:name/key}} — and the value is put in on the server at the moment the
// request is sent, for someone allowed to read that secret. A collection can
// therefore be shown to anyone who may call the service.
type SavedRequest struct {
	ID     string `json:"id"`
	Folder string `json:"folder,omitempty"`
	Name   string `json:"name"`
	Method string `json:"method"`
	// Port is the service port. Zero means the service's first.
	Port    int32    `json:"port,omitempty"`
	Path    string   `json:"path"`
	Headers []Header `json:"headers,omitempty"`
	Body    string   `json:"body,omitempty"`
}

// Collection is the saved requests of one service.
type Collection struct {
	Requests []SavedRequest `json:"requests"`
	// Version is the ConfigMap's resourceVersion, sent back with a save so
	// that two people saving at once do not silently overwrite each other.
	// Empty while nothing has been saved.
	Version string `json:"version"`
	// ReadOnly is set when something other than the portal manages the
	// ConfigMap — a chart, or GitOps. A change made here would be undone by
	// the next deploy, so none is offered.
	ReadOnly  bool   `json:"readOnly"`
	ManagedBy string `json:"managedBy,omitempty"`
}

// Methods a request may use.
var requestMethods = map[string]bool{
	http.MethodGet: true, http.MethodHead: true, http.MethodOptions: true,
	http.MethodPost: true, http.MethodPut: true, http.MethodPatch: true, http.MethodDelete: true,
}

// ReadOnlyMethod says whether a method only reads. Sending anything else needs
// the stronger grant.
func ReadOnlyMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions
}

var requestID = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)

func collectionName(service string) string { return requestsPrefix + service }

// RequestCollection returns the saved requests of a service.
func (m *Manager) RequestCollection(ctx context.Context, asUser, namespace, service string) (Collection, error) {
	cs, err := m.Clientset(asUser)
	if err != nil {
		return Collection{}, err
	}
	return requestCollection(ctx, cs, namespace, service)
}

func requestCollection(ctx context.Context, cs kubernetes.Interface, namespace, service string) (Collection, error) {
	cm, err := cs.CoreV1().ConfigMaps(namespace).Get(ctx, collectionName(service), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return Collection{Requests: []SavedRequest{}}, nil
	}
	if err != nil {
		return Collection{}, err
	}
	return readCollection(cm), nil
}

func readCollection(cm *corev1.ConfigMap) Collection {
	c := Collection{Requests: []SavedRequest{}, Version: cm.ResourceVersion}
	if by := cm.Labels[managedBy]; by != managedHere {
		c.ReadOnly, c.ManagedBy = true, by
	}
	for key, raw := range cm.Data {
		var r SavedRequest
		// A key that is not a request — written by hand, say — is left alone
		// rather than failing the whole collection.
		if !strings.HasSuffix(key, ".json") || json.Unmarshal([]byte(raw), &r) != nil {
			continue
		}
		r.ID = strings.TrimSuffix(key, ".json")
		c.Requests = append(c.Requests, r)
	}
	sort.Slice(c.Requests, func(i, j int) bool {
		a, b := c.Requests[i], c.Requests[j]
		if a.Folder != b.Folder {
			return a.Folder < b.Folder
		}
		return a.Name < b.Name
	})
	return c
}

// SaveRequests adds or replaces the given requests and removes the named ones,
// in one write. It returns the collection as it now stands, and a description
// of the change for the audit trail — request names, never their contents.
func (m *Manager) SaveRequests(ctx context.Context, asUser, namespace, service, version string, save []SavedRequest, remove []string) (Collection, string, error) {
	cs, err := m.Clientset(asUser)
	if err != nil {
		return Collection{}, "", err
	}
	return saveRequests(ctx, cs, namespace, service, version, save, remove)
}

func saveRequests(ctx context.Context, cs kubernetes.Interface, namespace, service, version string, save []SavedRequest, remove []string) (Collection, string, error) {
	cms := cs.CoreV1().ConfigMaps(namespace)
	cm, err := cms.Get(ctx, collectionName(service), metav1.GetOptions{})
	creating := apierrors.IsNotFound(err)
	switch {
	case creating && version != "":
		// It existed when the page loaded and has gone since.
		return Collection{}, "", ErrCollectionChanged
	case creating:
		cm = &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
			Name: collectionName(service), Namespace: namespace,
			Labels: map[string]string{managedBy: managedHere, requestsFor: service},
		}}
	case err != nil:
		return Collection{}, "", err
	case cm.ResourceVersion != version:
		return Collection{}, "", ErrCollectionChanged
	case cm.Labels[managedBy] != managedHere:
		return Collection{}, "", ErrCollectionReadOnly
	}
	if cm.Data == nil {
		cm.Data = map[string]string{}
	}

	var saved, removed []string
	for _, r := range save {
		if err := validRequest(&r); err != nil {
			return Collection{}, "", err
		}
		if r.ID == "" {
			r.ID = newRequestID()
		}
		id := r.ID
		r.ID = "" // the key carries it
		b, _ := json.Marshal(r)
		cm.Data[id+".json"] = string(b)
		saved = append(saved, r.Name)
	}
	for _, id := range remove {
		key := id + ".json"
		if raw, ok := cm.Data[key]; ok {
			var r SavedRequest
			_ = json.Unmarshal([]byte(raw), &r)
			removed = append(removed, r.Name)
			delete(cm.Data, key)
		}
	}

	size := 0
	for k, v := range cm.Data {
		size += len(k) + len(v)
	}
	if size > maxCollectionBytes {
		return Collection{}, "", ErrCollectionFull
	}

	if creating {
		cm, err = cms.Create(ctx, cm, metav1.CreateOptions{})
	} else {
		cm, err = cms.Update(ctx, cm, metav1.UpdateOptions{})
	}
	if apierrors.IsConflict(err) || apierrors.IsAlreadyExists(err) {
		return Collection{}, "", ErrCollectionChanged
	}
	if err != nil {
		return Collection{}, "", err
	}
	return readCollection(cm), describeSave(saved, removed), nil
}

func validRequest(r *SavedRequest) error {
	r.Method = strings.ToUpper(strings.TrimSpace(r.Method))
	r.Name = strings.TrimSpace(r.Name)
	r.Folder = strings.TrimSpace(r.Folder)
	switch {
	case r.ID != "" && !requestID.MatchString(r.ID):
		return fmt.Errorf("%w: %q is not a request id", ErrInvalidRequest, r.ID)
	case r.Name == "" || len(r.Name) > 200:
		return fmt.Errorf("%w: it needs a name, of up to 200 characters", ErrInvalidRequest)
	case len(r.Folder) > 200:
		return fmt.Errorf("%w: a folder name is at most 200 characters", ErrInvalidRequest)
	case !requestMethods[r.Method]:
		return fmt.Errorf("%w: %q is not a method it can use", ErrInvalidRequest, r.Method)
	case !strings.HasPrefix(r.Path, "/"):
		return fmt.Errorf("%w: the path must start with /", ErrInvalidRequest)
	}
	return nil
}

func newRequestID() string {
	b := make([]byte, 5)
	_, _ = rand.Read(b)
	return "r-" + hex.EncodeToString(b)
}

// describeSave names what changed. An import of dozens is counted, not
// listed: a line in the trail is for reading.
func describeSave(saved, removed []string) string {
	var parts []string
	if n := len(saved); n > 3 {
		parts = append(parts, fmt.Sprintf("saved %d requests", n))
	} else if n > 0 {
		parts = append(parts, "saved "+strings.Join(saved, ", "))
	}
	if n := len(removed); n > 3 {
		parts = append(parts, fmt.Sprintf("removed %d requests", n))
	} else if n > 0 {
		parts = append(parts, "removed "+strings.Join(removed, ", "))
	}
	return strings.Join(parts, "; ")
}
