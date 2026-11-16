package helmcat

import (
	"fmt"
	"reflect"
	"sort"
	"strings"

	yaml3 "gopkg.in/yaml.v3"
)

// What can be filled in from the cluster, rather than typed: where a chart
// takes a storage class, and where it takes an ingress. Charts name these
// differently, so they are found in the chart's own values, by the shapes the
// common charts use — Bitnami's, and the one `helm create` writes. A chart
// that uses neither is left to its values; its service can still be exposed
// from the Services page once it runs.

// Settings is what a chart's values let the portal fill in.
type Settings struct {
	// Storage are the paths that take a storage class, e.g.
	// global.defaultStorageClass or primary.persistence.storageClass.
	Storage []string `json:"storage"`
	// Ingresses are the ingress blocks the chart has.
	Ingresses []IngressSpot `json:"ingresses"`
}

// IngressSpot is one ingress block in a chart's values.
type IngressSpot struct {
	// Path is where the block is: "ingress", or "server.ingress".
	Path string `json:"path"`
	// Shape is how the block names a host: "hostname" (Bitnami, Argo CD:
	// hostname, extraTls), "hosts" (helm create: hosts[].host with paths,
	// tls[]) or "hostlist" (Grafana, Prometheus: hosts is a list of names,
	// with the path beside it).
	Shape string `json:"shape"`
	// ClassKey is the key the class goes under: ingressClassName or className.
	ClassKey string `json:"classKey"`
}

// maxDepth bounds the search: settings sit near the top of values, and
// examples buried in lists are not settings.
const maxDepth = 4

// Detect finds the settings in a chart's values.
func Detect(values map[string]any) Settings {
	s := Settings{Storage: []string{}, Ingresses: []IngressSpot{}}
	if g, ok := values["global"].(map[string]any); ok {
		for _, k := range []string{"defaultStorageClass", "storageClass"} {
			if _, ok := g[k]; ok {
				// Bitnami's global applies to every volume of the chart and
				// its subcharts: one setting is enough.
				s.Storage = append(s.Storage, "global."+k)
				break
			}
		}
	}
	// Each volume's own setting, used when there is no global one.
	var local []string
	var walk func(prefix string, m map[string]any, depth int)
	walk = func(prefix string, m map[string]any, depth int) {
		for k, v := range m {
			path := k
			if prefix != "" {
				path = prefix + "." + k
			}
			if prefix == "" && k == "global" {
				continue
			}
			if k == "storageClass" || k == "storageClassName" {
				if _, isMap := v.(map[string]any); !isMap {
					local = append(local, path)
					continue
				}
			}
			sub, ok := v.(map[string]any)
			if !ok {
				continue
			}
			if k == "ingress" {
				if spot, ok := ingressSpot(path, sub); ok {
					s.Ingresses = append(s.Ingresses, spot)
					continue
				}
			}
			if depth < maxDepth {
				walk(path, sub, depth+1)
			}
		}
	}
	walk("", values, 1)
	if len(s.Storage) == 0 {
		s.Storage = append(s.Storage, local...)
	}
	sort.Strings(s.Storage)
	sort.Slice(s.Ingresses, func(i, j int) bool {
		// The chart's own first, then by name.
		di, dj := strings.Count(s.Ingresses[i].Path, "."), strings.Count(s.Ingresses[j].Path, ".")
		if di != dj {
			return di < dj
		}
		return s.Ingresses[i].Path < s.Ingresses[j].Path
	})
	if len(s.Storage) > 12 {
		s.Storage = s.Storage[:12]
	}
	return s
}

func ingressSpot(path string, m map[string]any) (IngressSpot, bool) {
	if _, ok := m["enabled"]; !ok {
		return IngressSpot{}, false
	}
	spot := IngressSpot{Path: path}
	switch {
	case has(m, "hostname"):
		spot.Shape, spot.ClassKey = "hostname", "ingressClassName"
	case has(m, "hosts"):
		spot.Shape, spot.ClassKey = "hosts", "className"
		hosts, _ := m["hosts"].([]any)
		_, firstIsName := firstOf(hosts).(string)
		if firstIsName || (len(hosts) == 0 && (has(m, "paths") || has(m, "path"))) {
			// Grafana and the Prometheus charts take ingressClassName.
			spot.Shape, spot.ClassKey = "hostlist", "ingressClassName"
		}
	default:
		return IngressSpot{}, false
	}
	for _, k := range []string{"ingressClassName", "className"} {
		if has(m, k) {
			spot.ClassKey = k
		}
	}
	return spot, true
}

func firstOf(l []any) any {
	if len(l) == 0 {
		return nil
	}
	return l[0]
}

func has(m map[string]any, k string) bool {
	_, ok := m[k]
	return ok
}

// Fill is what to put into the values.
type Fill struct {
	StorageClass string       `json:"storageClass"`
	StoragePaths []string     `json:"storagePaths"`
	Ingress      *IngressFill `json:"ingress"`
}

// IngressFill is an ingress to switch on in one block.
type IngressFill struct {
	IngressSpot
	Host  string `json:"host"`
	Class string `json:"class"`
	// TLS is an existing secret holding the certificate; empty for none.
	TLS string `json:"tls"`
}

// assignments are the keys a fill sets, each with its value.
func (f Fill) assignments() (map[string]any, error) {
	out := map[string]any{}
	if f.StorageClass != "" {
		for _, p := range f.StoragePaths {
			out[p] = f.StorageClass
		}
	}
	in := f.Ingress
	if in == nil {
		return out, nil
	}
	if in.Host == "" {
		return nil, fmt.Errorf("the ingress needs a host")
	}
	block := map[string]any{"enabled": true}
	if in.Class != "" {
		key := in.ClassKey
		if key != "ingressClassName" && key != "className" {
			key = "ingressClassName"
		}
		block[key] = in.Class
	}
	tls := []any{map[string]any{"hosts": []any{in.Host}, "secretName": in.TLS}}
	switch in.Shape {
	case "hostname":
		block["hostname"] = in.Host
		if in.TLS != "" {
			block["extraTls"] = tls
		}
	case "hosts":
		block["hosts"] = []any{map[string]any{
			"host":  in.Host,
			"paths": []any{map[string]any{"path": "/", "pathType": "Prefix"}},
		}}
		if in.TLS != "" {
			block["tls"] = tls
		}
	case "hostlist":
		block["hosts"] = []any{in.Host}
		if in.TLS != "" {
			block["tls"] = tls
		}
	default:
		return nil, fmt.Errorf("unknown ingress shape %q", in.Shape)
	}
	for k, v := range block {
		out[in.Path+"."+k] = v
	}
	return out, nil
}

// ApplyFill brings values written as YAML from what an earlier fill, prev,
// wrote to what f writes now — the install form sends each change as it is
// made. Keys prev wrote and f does not are taken out again. A key whose value
// is no longer what prev wrote was changed by hand in the editor: it is left
// as it is, and named in kept. Everything else in the values, comments
// included, stays as it was written.
func ApplyFill(text string, f, prev Fill) (out string, kept []string, err error) {
	want, err := f.assignments()
	if err != nil {
		return "", nil, err
	}
	had, err := prev.assignments()
	if err != nil {
		had = map[string]any{}
	}

	var doc yaml3.Node
	if strings.TrimSpace(text) != "" {
		if err := yaml3.Unmarshal([]byte(text), &doc); err != nil {
			return "", nil, fmt.Errorf("your values are not YAML: %w", err)
		}
	}
	if len(doc.Content) == 0 {
		doc = yaml3.Node{Kind: yaml3.DocumentNode, Content: []*yaml3.Node{{Kind: yaml3.MappingNode, Tag: "!!map"}}}
	}
	root := doc.Content[0]
	if root.Kind != yaml3.MappingNode {
		if root.Tag != "!!null" {
			return "", nil, fmt.Errorf("your values are not a map of settings")
		}
		*root = yaml3.Node{Kind: yaml3.MappingNode, Tag: "!!map"}
	}

	// A key prev wrote is the form's while it still holds what prev wrote.
	ours := func(path string) bool {
		old, wrote := had[path]
		if !wrote {
			return true
		}
		cur := lookup(root, path)
		return cur == nil || sameValue(cur, old)
	}

	paths := make([]string, 0, len(had)+len(want))
	for p := range had {
		paths = append(paths, p)
	}
	for p := range want {
		if _, ok := had[p]; !ok {
			paths = append(paths, p)
		}
	}
	sort.Strings(paths)
	for _, p := range paths {
		v, keep := want[p]
		if !ours(p) {
			if keep || !sameValue(lookup(root, p), had[p]) {
				kept = append(kept, p)
			}
			continue
		}
		if !keep {
			remove(root, strings.Split(p, "."))
			continue
		}
		if cur := lookup(root, p); cur != nil && sameValue(cur, v) {
			continue
		}
		if err := setNode(root, p, v); err != nil {
			return "", nil, err
		}
	}

	if len(root.Content) == 0 {
		return "", kept, nil
	}
	var b strings.Builder
	enc := yaml3.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return "", nil, err
	}
	_ = enc.Close()
	return b.String(), kept, nil
}

// lookup finds the node at a.b.c, or nil.
func lookup(m *yaml3.Node, path string) *yaml3.Node {
	for _, k := range strings.Split(path, ".") {
		if m == nil || m.Kind != yaml3.MappingNode {
			return nil
		}
		m = child(m, k)
	}
	return m
}

// child is the value under key k of a mapping node, or nil.
func child(m *yaml3.Node, k string) *yaml3.Node {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == k {
			return m.Content[i+1]
		}
	}
	return nil
}

// sameValue says whether a node holds v, compared as data, not as text.
func sameValue(n *yaml3.Node, v any) bool {
	var have any
	if n == nil || n.Decode(&have) != nil {
		return false
	}
	raw, err := yaml3.Marshal(v)
	if err != nil {
		return false
	}
	var want any
	if yaml3.Unmarshal(raw, &want) != nil {
		return false
	}
	return reflect.DeepEqual(have, want)
}

// setNode sets a.b.c to v, making the maps on the way.
func setNode(m *yaml3.Node, path string, v any) error {
	parts := strings.Split(path, ".")
	for i, p := range parts {
		if p == "" {
			return fmt.Errorf("%q is not a path", path)
		}
		next := child(m, p)
		if i == len(parts)-1 {
			var val yaml3.Node
			if err := val.Encode(v); err != nil {
				return err
			}
			if next != nil {
				// Keep what was written beside the value.
				val.LineComment, val.HeadComment, val.FootComment = next.LineComment, next.HeadComment, next.FootComment
				*next = val
			} else {
				m.Content = append(m.Content, &yaml3.Node{Kind: yaml3.ScalarNode, Tag: "!!str", Value: p}, &val)
			}
			return nil
		}
		switch {
		case next == nil:
			next = &yaml3.Node{Kind: yaml3.MappingNode, Tag: "!!map"}
			m.Content = append(m.Content, &yaml3.Node{Kind: yaml3.ScalarNode, Tag: "!!str", Value: p}, next)
		case next.Kind == yaml3.MappingNode:
		case next.Tag == "!!null":
			*next = yaml3.Node{Kind: yaml3.MappingNode, Tag: "!!map"}
		default:
			return fmt.Errorf("%s is set to something that is not a map in your values", strings.Join(parts[:i+1], "."))
		}
		m = next
	}
	return nil
}

// remove takes out the key at the path, and any map left empty by it.
func remove(m *yaml3.Node, parts []string) {
	if m == nil || m.Kind != yaml3.MappingNode {
		return
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value != parts[0] {
			continue
		}
		if len(parts) > 1 {
			sub := m.Content[i+1]
			remove(sub, parts[1:])
			if sub.Kind != yaml3.MappingNode || len(sub.Content) > 0 {
				return
			}
		}
		m.Content = append(m.Content[:i], m.Content[i+2:]...)
		return
	}
}
