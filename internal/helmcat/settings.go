package helmcat

import (
	"fmt"
	"sort"
	"strings"

	"sigs.k8s.io/yaml"
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

// ApplyFill writes a fill into values written as YAML, and returns them as
// YAML. Keys already there are overwritten where the fill names them and kept
// otherwise.
func ApplyFill(text string, f Fill) (string, error) {
	vals := map[string]any{}
	if strings.TrimSpace(text) != "" {
		if err := yaml.Unmarshal([]byte(text), &vals); err != nil {
			return "", fmt.Errorf("your values are not YAML: %w", err)
		}
		if vals == nil {
			vals = map[string]any{}
		}
	}
	if f.StorageClass != "" {
		for _, p := range f.StoragePaths {
			if err := setPath(vals, p, f.StorageClass); err != nil {
				return "", err
			}
		}
	}
	if in := f.Ingress; in != nil {
		if in.Host == "" {
			return "", fmt.Errorf("the ingress needs a host")
		}
		block := map[string]any{"enabled": true}
		if in.Class != "" {
			key := in.ClassKey
			if key != "ingressClassName" && key != "className" {
				key = "ingressClassName"
			}
			block[key] = in.Class
		}
		switch in.Shape {
		case "hostname":
			block["hostname"] = in.Host
			if in.TLS != "" {
				block["extraTls"] = []any{map[string]any{"hosts": []any{in.Host}, "secretName": in.TLS}}
			}
		case "hosts":
			block["hosts"] = []any{map[string]any{
				"host":  in.Host,
				"paths": []any{map[string]any{"path": "/", "pathType": "Prefix"}},
			}}
			if in.TLS != "" {
				block["tls"] = []any{map[string]any{"hosts": []any{in.Host}, "secretName": in.TLS}}
			}
		case "hostlist":
			block["hosts"] = []any{in.Host}
			if in.TLS != "" {
				block["tls"] = []any{map[string]any{"hosts": []any{in.Host}, "secretName": in.TLS}}
			}
		default:
			return "", fmt.Errorf("unknown ingress shape %q", in.Shape)
		}
		for k, v := range block {
			if err := setPath(vals, in.Path+"."+k, v); err != nil {
				return "", err
			}
		}
	}
	out, err := yaml.Marshal(vals)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// setPath sets a.b.c, making the maps on the way.
func setPath(m map[string]any, path string, v any) error {
	parts := strings.Split(path, ".")
	for i, p := range parts {
		if p == "" {
			return fmt.Errorf("%q is not a path", path)
		}
		if i == len(parts)-1 {
			m[p] = v
			return nil
		}
		next, ok := m[p].(map[string]any)
		if !ok {
			if _, exists := m[p]; exists && m[p] != nil {
				return fmt.Errorf("%s is set to something that is not a map in your values", strings.Join(parts[:i+1], "."))
			}
			next = map[string]any{}
			m[p] = next
		}
		m = next
	}
	return nil
}
