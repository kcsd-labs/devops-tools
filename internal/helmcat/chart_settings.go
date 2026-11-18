package helmcat

import (
	"regexp"
	"sort"
	"strings"

	"helm.sh/helm/v3/pkg/chart"
)

// A chart is read as the person installing it sees it: one thing, whatever
// parts its author built it from. A subchart's values sit under its name in
// the parent's — postgresql.primary.persistence.size — and are asked about
// beside the parent's own; a part switched off is not asked about at all.

// Component is a part or a feature of a chart that is switched on or off.
type Component struct {
	// Key is the switch: flower.enabled, or a subchart's condition.
	Key string `json:"key"`
	On  bool   `json:"on"`
	// About is what the chart says of it.
	About string `json:"about"`
	// Part: a chart of its own, built in — a database, a cache.
	Part bool `json:"part,omitempty"`
}

// notComponents are switches that configure rather than add a part.
var notComponents = regexp.MustCompile(`(?i)^(ingress|global|rbac|serviceAccount|networkPolic(y|ies)|podSecurityPolicy|psp|podDisruptionBudget|pdb|autoscaling|hpa|tests?|persistence|serviceMonitor|prometheusRule|podMonitor|crds?|volumePermissions|sysctl|diagnosticMode|useExternalDNS)$`)

// maxComponents bounds the switches offered.
const maxComponents = 24

// DetectChart finds the settings of a chart and of the parts it is built
// from, and the switches that turn parts on or off.
func DetectChart(ch *chart.Chart) Settings {
	merged, parts, conds, every := mergedValues(ch)
	s := detect(merged, ownGlobalStorage(ch.Values))
	s.Parts = parts
	s.Components = components(merged, ChartDocs(ch), every, conds)
	return s
}

// ownGlobalStorage says whether the chart itself takes a storage class for
// every volume, as Bitnami's do.
func ownGlobalStorage(values map[string]any) bool {
	g, _ := values["global"].(map[string]any)
	return has(g, "defaultStorageClass") || has(g, "storageClass")
}

type dependency struct {
	key, cond string
	c         *chart.Chart
}

// dependencies are the subcharts a chart includes, each under its key.
func dependencies(ch *chart.Chart) []dependency {
	byName := map[string]*chart.Chart{}
	for _, d := range ch.Dependencies() {
		byName[d.Name()] = d
	}
	var out []dependency
	if ch.Metadata != nil && len(ch.Metadata.Dependencies) > 0 {
		for _, md := range ch.Metadata.Dependencies {
			c := byName[md.Name]
			if c == nil {
				continue
			}
			key := md.Alias
			if key == "" {
				key = md.Name
			}
			out = append(out, dependency{key, md.Condition, c})
		}
		return out
	}
	for _, d := range ch.Dependencies() {
		out = append(out, dependency{d.Name(), "", d})
	}
	return out
}

// mergedValues are a chart's values with each included subchart's under its
// key, the parent's own settings over the subchart's defaults. parts names
// each included subchart by what it says it is, every each subchart whether
// included or not; conds are their switches.
func mergedValues(ch *chart.Chart) (map[string]any, map[string]string, map[string]bool, map[string]string) {
	merged := deepCopy(ch.Values)
	parts := map[string]string{}
	every := map[string]string{}
	conds := map[string]bool{}
	for _, d := range dependencies(ch) {
		if d.c.Metadata != nil {
			every[d.key] = d.c.Metadata.Description
		} else {
			every[d.key] = ""
		}
		if d.cond != "" {
			if path, on, ok := condition(merged, d.cond); ok {
				conds[path] = on
				if !on {
					continue
				}
			}
		}
		sub := deepCopy(d.c.Values)
		g, _ := sub["global"].(map[string]any)
		delete(sub, "global")
		own, _ := merged[d.key].(map[string]any)
		merged[d.key] = mergeOver(sub, own)
		if g != nil {
			mg, _ := merged["global"].(map[string]any)
			merged["global"] = mergeOver(g, mg)
		}
		parts[d.key] = every[d.key]
	}
	return merged, parts, conds, every
}

// condition reads a subchart's condition — "postgresql.enabled", or a list
// of them, the first one set deciding — as Helm does.
func condition(values map[string]any, cond string) (string, bool, bool) {
	for _, path := range strings.Split(cond, ",") {
		path = strings.TrimSpace(path)
		var cur any = values
		for _, k := range strings.Split(path, ".") {
			m, ok := cur.(map[string]any)
			if !ok {
				cur = nil
				break
			}
			cur = m[k]
		}
		if b, ok := cur.(bool); ok {
			return path, b, true
		}
	}
	return "", false, false
}

// components are the switches of a chart: top-level blocks with an enabled
// of their own, and its subcharts' conditions.
func components(merged map[string]any, docs map[string]string, parts map[string]string, conds map[string]bool) []Component {
	seen := map[string]bool{}
	var out []Component
	add := func(key string, on bool, top string) {
		if seen[key] {
			return
		}
		seen[key] = true
		_, part := parts[top]
		about := docs[key]
		if about == "" {
			about = docs[top]
		}
		if about == "" {
			about = parts[top]
		}
		out = append(out, Component{Key: key, On: on, About: about, Part: part})
	}
	for path, on := range conds {
		add(path, on, strings.SplitN(path, ".", 2)[0])
	}
	for k, v := range merged {
		m, ok := v.(map[string]any)
		if !ok || notComponents.MatchString(k) {
			continue
		}
		if on, ok := m["enabled"].(bool); ok {
			add(k+".enabled", on, k)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		// What is on first, then built-in parts, then by name.
		if out[i].On != out[j].On {
			return out[i].On
		}
		if out[i].Part != out[j].Part {
			return out[i].Part
		}
		return out[i].Key < out[j].Key
	})
	if len(out) > maxComponents {
		out = out[:maxComponents]
	}
	return out
}

// ChartDocs are Docs of a chart and of the subcharts it includes, a
// subchart's keys under its own.
func ChartDocs(ch *chart.Chart) map[string]string {
	return withParts(ch, Docs)
}

// ChartExamples are Examples of a chart and of its subcharts.
func ChartExamples(ch *chart.Chart) map[string]string {
	return withParts(ch, Examples)
}

func withParts(ch *chart.Chart, read func([]byte) map[string]string) map[string]string {
	out := read(valuesFile(ch))
	_, parts, _, _ := mergedValues(ch)
	for _, d := range dependencies(ch) {
		if _, included := parts[d.key]; !included {
			continue
		}
		for k, v := range read(valuesFile(d.c)) {
			if strings.HasPrefix(k, "global.") || k == "global" {
				continue
			}
			p := d.key + "." + k
			if _, own := out[p]; !own && len(out) < maxDocs*2 {
				out[p] = v
			}
		}
	}
	return out
}

func valuesFile(ch *chart.Chart) []byte {
	for _, f := range ch.Raw {
		if f.Name == "values.yaml" {
			return f.Data
		}
	}
	return nil
}

// deepCopy copies a values map, maps inside it too.
func deepCopy(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		if sub, ok := v.(map[string]any); ok {
			out[k] = deepCopy(sub)
		} else {
			out[k] = v
		}
	}
	return out
}

// mergeOver lays over on top of base: base's keys, with over's where both
// have them, maps merged all the way down.
func mergeOver(base, over map[string]any) map[string]any {
	out := deepCopy(base)
	for k, v := range over {
		if om, ok := v.(map[string]any); ok {
			if bm, ok := out[k].(map[string]any); ok {
				out[k] = mergeOver(bm, om)
				continue
			}
		}
		out[k] = v
	}
	return out
}
