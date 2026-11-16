package kube

import (
	"regexp"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
)

// What a chart says when it will not render is written for someone reading
// helm's output: a template position first, then the chart's own words,
// sometimes several checks under one heading. Read apart, so the people
// installing see the words, one point each, and the position only on request.

var (
	executionRE = regexp.MustCompile(`(?s)^.*?execution error at \(([^)]*)\):\s*`)
	headingRE   = regexp.MustCompile(`(?m)^\s*CONFIGURATION CHECKS:\s*$`)
)

func parseRefusal(msg string) *Refusal {
	msg = strings.ReplaceAll(msg, "\r", "")
	msg = strings.TrimPrefix(msg, "INSTALLATION FAILED: ")
	msg = strings.TrimPrefix(msg, "UPGRADE FAILED: ")
	r := &Refusal{Messages: []string{}}
	if m := executionRE.FindStringSubmatch(msg); m != nil {
		r.Where = m[1]
		msg = msg[len(m[0]):]
	}
	msg = headingRE.ReplaceAllString(msg, "\n")
	msg = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(msg), "CONFIGURATION CHECKS:"))
	for _, para := range strings.Split(msg, "\n\n") {
		var words []string
		for _, l := range strings.Split(para, "\n") {
			if l = strings.TrimSpace(l); l != "" {
				words = append(words, l)
			}
		}
		if len(words) == 0 {
			continue
		}
		r.Messages = append(r.Messages, shorten(strings.Join(words, " "), 600))
		if len(r.Messages) == 8 {
			break
		}
	}
	if len(r.Messages) == 0 {
		r.Messages = append(r.Messages, shorten(strings.TrimSpace(msg), 600))
	}
	return r
}

// SecretHint is a Secret a chart can be pointed at: its name and the names
// of its keys — never what they hold.
type SecretHint struct {
	Name string   `json:"name"`
	Keys []string `json:"keys"`
}

// settingSecrets are the Secrets that hold settings — not Helm's own release
// records, not service account tokens, not certificates.
func settingSecrets(list []corev1.Secret) []SecretHint {
	out := []SecretHint{}
	for _, s := range list {
		if s.Type != corev1.SecretTypeOpaque && s.Type != corev1.SecretTypeBasicAuth && s.Type != "" {
			continue
		}
		if strings.HasPrefix(s.Name, "sh.helm.release.") {
			continue
		}
		keys := make([]string, 0, len(s.Data))
		for k := range s.Data {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out = append(out, SecretHint{Name: s.Name, Keys: keys})
		if len(out) == 200 {
			break
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
