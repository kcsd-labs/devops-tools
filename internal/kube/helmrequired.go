package kube

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// What a chart asks for when it will not render. Charts refuse in a handful
// of ways — their own `required` and `fail` messages, and the chart's JSON
// schema — and most name the key. Each named key becomes a field to fill in,
// instead of a line of template error to decipher.

var (
	// "Please set certmanager-issuer.email", "you must set x.y",
	// "a value for x.y is required", "x.y is required".
	requiredREs = []*regexp.Regexp{
		regexp.MustCompile(`(?i)please (?:set|provide|specify) (?:a value for |the )?['"\x60]?([A-Za-z][\w-]*(?:\.[\w-]+)+)`),
		regexp.MustCompile(`(?i)you must (?:set|provide|specify) (?:a value for |the )?['"\x60]?([A-Za-z][\w-]*(?:\.[\w-]+)+)`),
		regexp.MustCompile(`(?i)(?:a )?value for ['"\x60]?([A-Za-z][\w-]*(?:\.[\w-]+)*)['"\x60]? (?:is|must be) (?:required|set|provided)`),
		regexp.MustCompile(`(?i)['"\x60]?([A-Za-z][\w-]*(?:\.[\w-]+)+)['"\x60]? (?:is|are) (?:required|mandatory)`),
	}
	// A template reading a key under a map the values do not have:
	// "at <.Values.db.password.secret>: nil pointer evaluating".
	nilMapRE = regexp.MustCompile(`at <\$?\.Values\.([A-Za-z][\w.-]*)>: nil pointer evaluating`)
	// The schema, as Helm 3.18 and later word it:
	// "- at '/auth': missing property 'password'".
	schemaAtRE = regexp.MustCompile(`at '([^']*)': missing propert(?:y|ies) ((?:'[^']+'(?:,\s*)?)+)`)
	// And as earlier Helm did: "- auth: password is required".
	schemaOldRE = regexp.MustCompile(`^- ([\w.()-]+): ([\w-]+) is required`)
	// "mychart:" heading the schema errors of one chart, or a subchart.
	schemaChartRE = regexp.MustCompile(`^([\w-]+):$`)
	quotedRE      = regexp.MustCompile(`'([^']+)'`)
)

// requiredValues reads the keys a render error asks for. top is the chart's
// name: schema errors of a subchart are under the subchart's key.
func requiredValues(msg, top string) []RequiredValue {
	var out []RequiredValue
	seen := map[string]bool{}
	add := func(key, said string) {
		key = strings.Trim(key, ".")
		if key == "" || seen[key] {
			return
		}
		seen[key] = true
		out = append(out, RequiredValue{Key: key, Said: shorten(strings.TrimSpace(said), 300)})
	}
	sub := ""
	for _, line := range strings.Split(msg, "\n") {
		line = strings.TrimSpace(line)
		if m := schemaChartRE.FindStringSubmatch(line); m != nil {
			sub = ""
			if m[1] != top {
				sub = m[1]
			}
			continue
		}
		under := func(path string) string {
			if sub == "" {
				return path
			}
			if path == "" {
				return sub
			}
			return sub + "." + path
		}
		if m := schemaAtRE.FindStringSubmatch(line); m != nil {
			parent := strings.ReplaceAll(strings.Trim(m[1], "/"), "/", ".")
			for _, q := range quotedRE.FindAllStringSubmatch(m[2], -1) {
				key := q[1]
				if p := under(parent); p != "" {
					key = p + "." + key
				}
				add(key, "the chart's schema needs it")
			}
			continue
		}
		if m := schemaOldRE.FindStringSubmatch(line); m != nil {
			parent := strings.TrimPrefix(m[1], "(root)")
			key := m[2]
			if p := under(strings.Trim(parent, ".")); p != "" {
				key = p + "." + key
			}
			add(key, "the chart's schema needs it")
			continue
		}
		if m := nilMapRE.FindStringSubmatch(line); m != nil {
			add(m[1], "The chart reads it, and the values do not have the block it sits in.")
			continue
		}
		for _, re := range requiredREs {
			for _, m := range re.FindAllStringSubmatch(line, -1) {
				add(m[1], sentenceAround(line, m[0]))
			}
		}
	}
	return out
}

// sentenceAround is the part of an error line that says what is wanted:
// after the template position Helm puts in front.
func sentenceAround(line, match string) string {
	if i := strings.LastIndex(line, "): "); i >= 0 && i < strings.Index(line, match) {
		return line[i+3:]
	}
	return line
}

// NamespaceExists reports whether the cluster has the namespace.
func (m *Manager) NamespaceExists(ctx context.Context, name string) (bool, error) {
	cs, err := m.Clientset("")
	if err != nil {
		return false, err
	}
	_, err = cs.CoreV1().Namespaces().Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// CreateNamespace makes a namespace for a release from the catalog, marked as
// the portal's and with who asked for it. One already there is left as it is.
func (m *Manager) CreateNamespace(ctx context.Context, name, requestedBy string) error {
	cs, err := m.Clientset("")
	if err != nil {
		return err
	}
	_, err = cs.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Labels:      map[string]string{"app.kubernetes.io/managed-by": "devops-tools"},
			Annotations: map[string]string{"devops-tools/requested-by": requestedBy},
		},
	}, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		return nil
	}
	return err
}

// SecretExists reports whether a namespace has a Secret by that name; a
// namespace that is not there has none.
func (m *Manager) SecretExists(ctx context.Context, namespace, name string) (bool, error) {
	cs, err := m.Clientset("")
	if err != nil {
		return false, err
	}
	_, err = cs.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// CreateSecretFor creates a Secret a release from the catalog was asked
// with, marked as the portal's and the release's.
func (m *Manager) CreateSecretFor(ctx context.Context, namespace, name string, data map[string]string, release string) error {
	cs, err := m.Clientset("")
	if err != nil {
		return err
	}
	_, err = cs.CoreV1().Secrets(namespace).Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   namespace,
			Labels:      map[string]string{"app.kubernetes.io/managed-by": "devops-tools"},
			Annotations: map[string]string{"devops-tools/created-for-release": release},
		},
		Type:       corev1.SecretTypeOpaque,
		StringData: data,
	}, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("a Secret named %s is already in %s", name, namespace)
	}
	return err
}
