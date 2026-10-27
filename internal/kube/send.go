package kube

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	// sendTimeout bounds one request. Long enough for a slow endpoint, short
	// enough that a hung one does not hold a connection open for minutes.
	sendTimeout = 30 * time.Second
	// maxResponseBytes is as much of a response as is sent back to the
	// browser. The rest is counted, not kept.
	maxResponseBytes = 1 << 20
	// maxDocBytes bounds an OpenAPI document.
	maxDocBytes = 5 << 20
)

var (
	ErrOutsideCluster = errors.New("this service is an alias for a host outside the cluster; requests are only made to services inside it")
	ErrUnknownPort    = errors.New("the service has no such port")
	ErrNoAPIDocs      = errors.New("the service publishes no OpenAPI document at the usual paths")
)

// OutgoingRequest is a request as the page sends it. Only the method, the
// port, the path and what is sent change; the host is always the service.
type OutgoingRequest struct {
	Method  string   `json:"method"`
	Port    int32    `json:"port"`
	Path    string   `json:"path"`
	Headers []Header `json:"headers"`
	Body    string   `json:"body"`
}

// Response is what came back. A request that got no answer is a response
// too: Error says why, and Status is zero.
type Response struct {
	URL        string   `json:"url"`
	Status     int      `json:"status,omitempty"`
	StatusText string   `json:"statusText,omitempty"`
	Millis     int64    `json:"ms"`
	Headers    []Header `json:"headers,omitempty"`
	Body       string   `json:"body,omitempty"`
	// Base64 is set when the body is not text.
	Base64 bool `json:"base64,omitempty"`
	// Truncated is set when the body was longer than maxResponseBytes.
	Truncated bool   `json:"truncated,omitempty"`
	Size      int64  `json:"size"`
	Error     string `json:"error,omitempty"`
}

// SecretResolver gives the value of one key of a secret in the service's
// namespace, or refuses to.
type SecretResolver func(name, key string) (string, error)

// SendRequest sends a request to a service, from this pod, through the
// service's own address.
//
// The caller chooses the method, the path, the headers and the body — never
// the host. The path is checked to be a path, so nothing in it can point the
// request somewhere other than the service.
func (m *Manager) SendRequest(ctx context.Context, asUser, namespace, service string, r OutgoingRequest, secrets SecretResolver) (Response, error) {
	cs, err := m.Clientset(asUser)
	if err != nil {
		return Response{}, err
	}
	svc, err := cs.CoreV1().Services(namespace).Get(ctx, service, metav1.GetOptions{})
	if err != nil {
		return Response{}, err
	}
	base, err := serviceBase(svc, r.Port)
	if err != nil {
		return Response{}, err
	}
	target, err := targetURL(base, r.Path)
	if err != nil {
		return Response{}, err
	}
	headers := make([]Header, 0, len(r.Headers))
	for _, h := range r.Headers {
		v, err := fillSecrets(h.Value, secrets)
		if err != nil {
			return Response{}, err
		}
		headers = append(headers, Header{Name: h.Name, Value: v})
	}
	body, err := fillSecrets(r.Body, secrets)
	if err != nil {
		return Response{}, err
	}
	return send(ctx, r.Method, target, headers, body), nil
}

// serviceBase is scheme://name.namespace.svc.<domain>:port for one of the
// service's ports, or its first when port is zero.
func serviceBase(svc *corev1.Service, port int32) (string, error) {
	if svc.Spec.Type == corev1.ServiceTypeExternalName {
		return "", ErrOutsideCluster
	}
	var sp *corev1.ServicePort
	for i := range svc.Spec.Ports {
		if port == 0 || svc.Spec.Ports[i].Port == port {
			sp = &svc.Spec.Ports[i]
			break
		}
	}
	if sp == nil {
		return "", ErrUnknownPort
	}
	scheme := "http"
	hint := strings.ToLower(sp.Name)
	if sp.AppProtocol != nil && *sp.AppProtocol != "" {
		hint = strings.ToLower(*sp.AppProtocol)
	}
	if strings.HasPrefix(hint, "https") || (hint == "" && sp.Port == 443) {
		scheme = "https"
	}
	return fmt.Sprintf("%s://%s.%s.svc.%s:%d", scheme, svc.Name, svc.Namespace, ClusterDomain(), sp.Port), nil
}

// targetURL puts a path and query on the service's address. Anything that is
// not a path — a scheme, a host, "//elsewhere" — is refused rather than
// cleaned up: there is no reading of such a thing that should be sent.
func targetURL(base, path string) (string, error) {
	if !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") || strings.ContainsAny(path, " \t\r\n\\") {
		return "", errors.New("the path must start with a single / and contain no spaces")
	}
	ref, err := url.Parse(path)
	if err != nil {
		return "", fmt.Errorf("the path does not parse: %w", err)
	}
	if ref.Scheme != "" || ref.Host != "" || ref.User != nil {
		return "", errors.New("the path must be a path, not an address")
	}
	u, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	u.Path, u.RawPath, u.RawQuery = ref.Path, ref.RawPath, ref.RawQuery
	return u.String(), nil
}

var secretRef = regexp.MustCompile(`\{\{\s*secret:([a-z0-9]([-a-z0-9.]*[a-z0-9])?)/([-._a-zA-Z0-9]+)\s*\}\}`)

// fillSecrets replaces each {{secret:name/key}} with the value it names.
func fillSecrets(s string, secrets SecretResolver) (string, error) {
	if !strings.Contains(s, "{{") {
		return s, nil
	}
	var failed error
	out := secretRef.ReplaceAllStringFunc(s, func(ref string) string {
		m := secretRef.FindStringSubmatch(ref)
		if failed != nil {
			return ""
		}
		if secrets == nil {
			failed = errors.New("secrets cannot be used here")
			return ""
		}
		v, err := secrets(m[1], m[3])
		if err != nil {
			failed = fmt.Errorf("{{secret:%s/%s}}: %w", m[1], m[3], err)
			return ""
		}
		return v
	})
	return out, failed
}

// sendClient is built for calls inside the cluster.
var sendClient = &http.Client{
	Timeout: sendTimeout,
	Transport: &http.Transport{
		// No proxy: one configured for reaching the internet would send a
		// cluster-internal name somewhere that cannot resolve it.
		Proxy: nil,
		// Services inside a cluster commonly present certificates nothing
		// outside it would trust, and the person sending is debugging the
		// service, not its certificate.
		TLSClientConfig:   &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // see above
		DisableKeepAlives: true,
	},
	// A redirect is shown, not followed: following it could lead out of the
	// cluster, which is exactly what fixing the host is there to prevent.
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// Headers the transport sets itself, or that would change where or how the
// request goes.
var droppedHeaders = map[string]bool{
	"host": true, "content-length": true, "connection": true, "transfer-encoding": true,
	"upgrade": true, "proxy-authorization": true, "proxy-connection": true, "te": true, "trailer": true,
}

func send(ctx context.Context, method, target string, headers []Header, body string) Response {
	res := Response{URL: target}
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, rd)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	req.Header.Set("User-Agent", "devops-tools")
	for _, h := range headers {
		name := strings.TrimSpace(h.Name)
		if name == "" || droppedHeaders[strings.ToLower(name)] {
			continue
		}
		req.Header.Add(name, h.Value)
	}

	start := time.Now()
	resp, err := sendClient.Do(req)
	if err != nil {
		res.Millis = time.Since(start).Milliseconds()
		res.Error = describeCallError(err)
		return res
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	n, _ := io.Copy(&buf, io.LimitReader(resp.Body, maxResponseBytes))
	rest, _ := io.Copy(io.Discard, resp.Body)
	res.Millis = time.Since(start).Milliseconds()

	res.Status, res.StatusText = resp.StatusCode, http.StatusText(resp.StatusCode)
	res.Size, res.Truncated = n+rest, rest > 0
	if utf8.Valid(buf.Bytes()) {
		res.Body = buf.String()
	} else {
		res.Body, res.Base64 = base64.StdEncoding.EncodeToString(buf.Bytes()), true
	}
	for name, values := range resp.Header {
		for _, v := range values {
			res.Headers = append(res.Headers, Header{Name: name, Value: v})
		}
	}
	sort.SliceStable(res.Headers, func(i, j int) bool { return res.Headers[i].Name < res.Headers[j].Name })
	return res
}

// describeCallError says why a call got no answer, in the words someone
// debugging it would use.
func describeCallError(err error) string {
	var dns *net.DNSError
	var ne net.Error
	switch {
	case errors.Is(err, syscall.ECONNREFUSED):
		return "connection refused"
	case errors.As(err, &dns):
		return "the name does not resolve"
	case errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()):
		return fmt.Sprintf("no answer within %d s — if the service is up, a NetworkPolicy may be keeping this portal out", int(sendTimeout.Seconds()))
	case errors.Is(err, syscall.ECONNRESET):
		return "connection reset"
	}
	return err.Error()
}

// APIEndpoint is one operation from a service's OpenAPI document.
type APIEndpoint struct {
	Method  string `json:"method"`
	Path    string `json:"path"`
	Summary string `json:"summary,omitempty"`
	Tag     string `json:"tag,omitempty"`
}

// The paths an OpenAPI document is usually published under: springdoc,
// springfox, and the names most other frameworks use.
var apiDocPaths = []string{"/v3/api-docs", "/v2/api-docs", "/openapi.json", "/swagger.json", "/openapi.yaml"}

// ServiceAPI reads the operations a service describes in its OpenAPI document,
// trying the usual paths in turn. It returns the path the document was found
// at.
func (m *Manager) ServiceAPI(ctx context.Context, asUser, namespace, service string, port int32) (string, []APIEndpoint, error) {
	cs, err := m.Clientset(asUser)
	if err != nil {
		return "", nil, err
	}
	svc, err := cs.CoreV1().Services(namespace).Get(ctx, service, metav1.GetOptions{})
	if err != nil {
		return "", nil, err
	}
	base, err := serviceBase(svc, port)
	if err != nil {
		return "", nil, err
	}
	for _, p := range apiDocPaths {
		eps, ok := fetchAPIDoc(ctx, base+p)
		if ok {
			return p, eps, nil
		}
	}
	return "", nil, ErrNoAPIDocs
}

func fetchAPIDoc(ctx context.Context, target string) ([]APIEndpoint, bool) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, false
	}
	req.Header.Set("Accept", "application/json")
	resp, err := sendClient.Do(req)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, false
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxDocBytes))
	if err != nil {
		return nil, false
	}
	eps, err := parseAPIDoc(b)
	return eps, err == nil && len(eps) > 0
}

// parseAPIDoc lists the operations in an OpenAPI 3 or Swagger 2 document
// written as JSON. Paths are given as the service answers them: prefixed with
// the server's base path where the document declares one.
func parseAPIDoc(b []byte) ([]APIEndpoint, error) {
	var doc struct {
		BasePath string `json:"basePath"`
		Servers  []struct {
			URL string `json:"url"`
		} `json:"servers"`
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, err
	}
	prefix := doc.BasePath
	if len(doc.Servers) > 0 {
		if u, err := url.Parse(doc.Servers[0].URL); err == nil {
			prefix = u.Path
		}
	}
	prefix = strings.TrimSuffix(prefix, "/")

	var out []APIEndpoint
	for path, ops := range doc.Paths {
		for method, raw := range ops {
			m := strings.ToUpper(method)
			if !requestMethods[m] {
				continue // "parameters", "summary" and the like sit beside the methods
			}
			var op struct {
				Summary     string   `json:"summary"`
				OperationID string   `json:"operationId"`
				Tags        []string `json:"tags"`
			}
			_ = json.Unmarshal(raw, &op)
			e := APIEndpoint{Method: m, Path: prefix + path, Summary: op.Summary}
			if e.Summary == "" {
				e.Summary = op.OperationID
			}
			if len(op.Tags) > 0 {
				e.Tag = op.Tags[0]
			}
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Tag != b.Tag {
			return a.Tag < b.Tag
		}
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		return a.Method < b.Method
	})
	return out, nil
}
