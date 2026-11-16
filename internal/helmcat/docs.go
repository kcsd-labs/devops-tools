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
