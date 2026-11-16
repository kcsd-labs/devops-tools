package helmcat

import (
	"regexp"
	"strings"

	yaml3 "gopkg.in/yaml.v3"
)

// What a chart says about its own values, so the install dialog can explain
// a key where it is written. Charts document their values in comments, in
// one of three ways: Bitnami's "## @param path description" lines, the
// "# -- description" helm-docs reads, or a plain comment above the key.

// maxDocs bounds how many keys are described: a chart like GitLab's has
// thousands, and the editor needs the ones near the top far more.
const maxDocs = 4000

var paramRE = regexp.MustCompile(`^\s*#+\s*@param\s+(\S+)\s+(.*)$`)

// Docs maps every key of a chart's values.yaml, a.b.c, to what the chart
// says about it — empty when it says nothing. Keys inside lists are left out.
func Docs(raw []byte) map[string]string {
	out := map[string]string{}
	var doc yaml3.Node
	if yaml3.Unmarshal(raw, &doc) != nil || len(doc.Content) == 0 {
		return out
	}
	var walk func(prefix string, m *yaml3.Node, depth int)
	walk = func(prefix string, m *yaml3.Node, depth int) {
		if m.Kind != yaml3.MappingNode || depth > 8 {
			return
		}
		for i := 0; i+1 < len(m.Content) && len(out) < maxDocs; i += 2 {
			k, v := m.Content[i], m.Content[i+1]
			path := k.Value
			if prefix != "" {
				path = prefix + "." + k.Value
			}
			text := commentText(k.HeadComment)
			if text == "" {
				text = commentText(v.LineComment)
			}
			if text == "" {
				text = commentText(k.LineComment)
			}
			out[path] = text
			walk(path, v, depth+1)
		}
	}
	walk("", doc.Content[0], 1)

	// Bitnami lists its parameters apart from the keys; those win.
	for _, line := range strings.Split(string(raw), "\n") {
		if m := paramRE.FindStringSubmatch(line); m != nil {
			if _, known := out[m[1]]; known || len(out) < maxDocs {
				out[m[1]] = clip(strings.TrimSpace(m[2]))
			}
		}
	}
	return out
}

var (
	realKeyRE      = regexp.MustCompile(`^(\s*)([A-Za-z0-9_.-]+):(\s|$)`)
	commentedKeyRE = regexp.MustCompile(`^(\s*)#\s?([a-z][A-Za-z0-9_-]*):(?:\s+([^#]*?))?\s*(#.*)?$`)
)

// Examples are the settings a chart's values.yaml shows only commented out —
// "# username: gitlab" under psql — each with the value it shows. They are
// the optional ones: a chart that needs a host and a password says so, but
// not that it also takes a user name and a database.
func Examples(raw []byte) map[string]string {
	out := map[string]string{}
	type level struct {
		indent int
		key    string
	}
	var stack []level
	path := func(upto int) []string {
		var p []string
		for _, l := range stack[:upto] {
			p = append(p, l.key)
		}
		return p
	}
	for _, line := range strings.Split(strings.ReplaceAll(string(raw), "\r", ""), "\n") {
		if m := realKeyRE.FindStringSubmatch(line); m != nil {
			indent := len(m[1])
			for len(stack) > 0 && stack[len(stack)-1].indent >= indent {
				stack = stack[:len(stack)-1]
			}
			stack = append(stack, level{indent, m[2]})
			continue
		}
		m := commentedKeyRE.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		indent := len(m[1])
		// Under the deepest real key less indented than the comment.
		n := len(stack)
		for n > 0 && stack[n-1].indent >= indent {
			n--
		}
		if n == 0 && indent > 0 {
			continue
		}
		value := strings.TrimSpace(m[3])
		if len(strings.Fields(value)) > 3 {
			continue // words, not a value
		}
		p := strings.Join(append(path(n), m[2]), ".")
		if _, ok := out[p]; !ok && len(out) < maxDocs {
			out[p] = value
		}
	}
	return out
}

// commentText is a comment block as prose: the # marks and helm-docs' "--"
// taken off, Bitnami's section markers and commented-out examples dropped.
func commentText(c string) string {
	var lines []string
	for _, l := range strings.Split(c, "\n") {
		l = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(l), "#"))
		if l == "" {
			continue
		}
		if strings.HasPrefix(l, "@") {
			// @section, @skip, @extra, and @param lines read separately.
			continue
		}
		if looksLikeYAML(l) {
			continue
		}
		l = strings.TrimSpace(strings.TrimPrefix(l, "--"))
		if strings.HasPrefix(l, "ref:") || strings.HasPrefix(l, "e.g:") {
			continue
		}
		lines = append(lines, l)
	}
	return clip(strings.Join(lines, " "))
}

var yamlishRE = regexp.MustCompile(`^(- )?[A-Za-z0-9_.-]+:(\s|$)`)

// looksLikeYAML is a commented-out example rather than words: "key: value".
func looksLikeYAML(l string) bool {
	return yamlishRE.MatchString(l) && !strings.Contains(l, ". ") && len(strings.Fields(l)) <= 4
}

func clip(s string) string {
	if len(s) > 400 {
		return s[:400] + "…"
	}
	return s
}
