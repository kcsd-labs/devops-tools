package kube

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestSavedRequestsRoundTrip(t *testing.T) {
	ctx := context.Background()
	cs := fake.NewSimpleClientset()

	empty, err := requestCollection(ctx, cs, testNS, "api")
	if err != nil || len(empty.Requests) != 0 || empty.Version != "" {
		t.Fatalf("empty collection = %+v, %v", empty, err)
	}

	c, detail, err := saveRequests(ctx, cs, testNS, "api", "", []SavedRequest{
		{Folder: "Payments", Name: "Create payment", Method: "post", Path: "/api/v1/payments",
			Headers: []Header{{Name: "Authorization", Value: "Bearer {{secret:api-creds/token}}"}}, Body: `{"amount":1}`},
		{Name: "Health", Method: "GET", Path: "/actuator/health"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Requests) != 2 || c.Requests[0].Name != "Health" || c.Requests[1].Method != "POST" {
		t.Fatalf("collection = %+v", c.Requests)
	}
	if detail != "saved Create payment, Health" {
		t.Errorf("detail = %q", detail)
	}
	cm, _ := cs.CoreV1().ConfigMaps(testNS).Get(ctx, "devops-tools-requests-api", metav1.GetOptions{})
	if cm.Labels[managedBy] != managedHere || cm.Labels[requestsFor] != "api" {
		t.Errorf("labels = %v", cm.Labels)
	}

	// Removing by id, against the version just read.
	health := c.Requests[0].ID
	c, detail, err = saveRequests(ctx, cs, testNS, "api", c.Version, nil, []string{health})
	if err != nil || len(c.Requests) != 1 || detail != "removed Health" {
		t.Fatalf("after remove: %+v, %q, %v", c.Requests, detail, err)
	}
}

func TestAStaleSaveIsRefused(t *testing.T) {
	ctx := context.Background()
	cs := fake.NewSimpleClientset(&corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name: "devops-tools-requests-api", Namespace: testNS, ResourceVersion: "7",
			Labels: map[string]string{managedBy: managedHere},
		},
	})
	_, _, err := saveRequests(ctx, cs, testNS, "api", "6", []SavedRequest{{Name: "x", Method: "GET", Path: "/"}}, nil)
	if !errors.Is(err, ErrCollectionChanged) {
		t.Fatalf("err = %v, want ErrCollectionChanged", err)
	}
}

func TestACollectionManagedElsewhereIsReadOnly(t *testing.T) {
	ctx := context.Background()
	cs := fake.NewSimpleClientset(&corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name: "devops-tools-requests-api", Namespace: testNS, ResourceVersion: "3",
			Labels: map[string]string{managedBy: "Helm"},
		},
		Data: map[string]string{"r-1.json": `{"name":"Ping","method":"GET","path":"/ping"}`, "notes.txt": "hand-written"},
	})
	c, err := requestCollection(ctx, cs, testNS, "api")
	if err != nil || !c.ReadOnly || c.ManagedBy != "Helm" || len(c.Requests) != 1 || c.Requests[0].ID != "r-1" {
		t.Fatalf("collection = %+v, %v", c, err)
	}
	_, _, err = saveRequests(ctx, cs, testNS, "api", "3", []SavedRequest{{Name: "x", Method: "GET", Path: "/"}}, nil)
	if !errors.Is(err, ErrCollectionReadOnly) {
		t.Fatalf("err = %v, want ErrCollectionReadOnly", err)
	}
}

func TestABadRequestIsNotSaved(t *testing.T) {
	cs := fake.NewSimpleClientset()
	for _, r := range []SavedRequest{
		{Name: "", Method: "GET", Path: "/"},
		{Name: "x", Method: "TRACE", Path: "/"},
		{Name: "x", Method: "GET", Path: "relative"},
		{ID: "../evil", Name: "x", Method: "GET", Path: "/"},
	} {
		if _, _, err := saveRequests(context.Background(), cs, testNS, "api", "", []SavedRequest{r}, nil); !errors.Is(err, ErrInvalidRequest) {
			t.Errorf("%+v: err = %v, want ErrInvalidRequest", r, err)
		}
	}
}

func TestOnlyAPathCanBeAddedToTheServiceAddress(t *testing.T) {
	base := "http://api.payments-dev.svc.cluster.local:8080"
	good := map[string]string{
		"/":                       base + "/",
		"/api/v1/payments?page=2": base + "/api/v1/payments?page=2",
		"/a%2Fb":                  base + "/a%2Fb",
	}
	for in, want := range good {
		if got, err := targetURL(base, in); err != nil || got != want {
			t.Errorf("targetURL(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{
		"", "api", "//evil.example/x", "http://evil.example/", "/x y", "/\\evil", "/x\r\nHost: evil",
	} {
		if got, err := targetURL(base, bad); err == nil {
			t.Errorf("targetURL(%q) = %q, want refused", bad, got)
		}
	}
}

func TestSecretsAreFilledInOnlyThroughTheResolver(t *testing.T) {
	resolve := func(name, key string) (string, error) {
		if name == "api-creds" && key == "token" {
			return "s3cret", nil
		}
		return "", errors.New("no such secret")
	}
	got, err := fillSecrets("Bearer {{secret:api-creds/token}}", resolve)
	if err != nil || got != "Bearer s3cret" {
		t.Fatalf("got %q, %v", got, err)
	}
	if _, err := fillSecrets("{{ secret:other/key }}", resolve); err == nil {
		t.Fatal("an unknown secret was not refused")
	}
	if got, _ := fillSecrets("{{baseUrl}}/x", resolve); got != "{{baseUrl}}/x" {
		t.Fatalf("a non-secret placeholder changed: %q", got)
	}
}

func TestSendReportsTheAnswerAndDropsHostHeaders(t *testing.T) {
	var seenHost, seenAuth, seenBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenHost, seenAuth = r.Host, r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		seenBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":1}`))
	}))
	defer srv.Close()

	res := send(context.Background(), "POST", srv.URL+"/x",
		[]Header{{Name: "Host", Value: "evil.example"}, {Name: "Authorization", Value: "Bearer t"}}, `{"a":1}`)
	if res.Status != 201 || res.Body != `{"id":1}` || res.Error != "" {
		t.Fatalf("response = %+v", res)
	}
	if strings.Contains(seenHost, "evil") || seenAuth != "Bearer t" || seenBody != `{"a":1}` {
		t.Fatalf("server saw host %q, auth %q, body %q", seenHost, seenAuth, seenBody)
	}
}

func TestALargeResponseIsCut(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("a", maxResponseBytes+10)))
	}))
	defer srv.Close()
	res := send(context.Background(), "GET", srv.URL, nil, "")
	if !res.Truncated || len(res.Body) != maxResponseBytes || res.Size != maxResponseBytes+10 {
		t.Fatalf("truncated=%v len=%d size=%d", res.Truncated, len(res.Body), res.Size)
	}
}

func TestAPIDocsAreReadFromOpenAPIAndSwagger(t *testing.T) {
	v3 := `{"openapi":"3.0.1","servers":[{"url":"http://localhost:8080/api"}],
	  "paths":{"/payments":{"get":{"summary":"List","tags":["payments"]},"post":{"operationId":"create","tags":["payments"]},
	  "parameters":[]}}}`
	eps, err := parseAPIDoc([]byte(v3))
	if err != nil || len(eps) != 2 {
		t.Fatalf("v3: %+v, %v", eps, err)
	}
	if eps[0] != (APIEndpoint{Method: "GET", Path: "/api/payments", Summary: "List", Tag: "payments"}) ||
		eps[1].Summary != "create" {
		t.Errorf("v3 endpoints = %+v", eps)
	}

	v2 := `{"swagger":"2.0","basePath":"/v1","paths":{"/ping":{"get":{}}}}`
	eps, err = parseAPIDoc([]byte(v2))
	if err != nil || len(eps) != 1 || eps[0].Path != "/v1/ping" {
		t.Fatalf("v2: %+v, %v", eps, err)
	}
}

func TestServiceBaseUsesTheRightPortAndScheme(t *testing.T) {
	https := "https"
	svc := service("api", func(s *corev1.Service) {
		s.Spec.Ports = []corev1.ServicePort{{Name: "http", Port: 8080}, {Name: "admin", Port: 9443, AppProtocol: &https}}
	})
	if got, _ := serviceBase(svc, 0); got != "http://api.payments-dev.svc."+ClusterDomain()+":8080" {
		t.Errorf("default port = %q", got)
	}
	if got, _ := serviceBase(svc, 9443); !strings.HasPrefix(got, "https://") {
		t.Errorf("https port = %q", got)
	}
	if _, err := serviceBase(svc, 1234); !errors.Is(err, ErrUnknownPort) {
		t.Errorf("unknown port: %v", err)
	}
	ext := service("ext", func(s *corev1.Service) { s.Spec.Type = corev1.ServiceTypeExternalName })
	if _, err := serviceBase(ext, 0); !errors.Is(err, ErrOutsideCluster) {
		t.Errorf("external name: %v", err)
	}
}
