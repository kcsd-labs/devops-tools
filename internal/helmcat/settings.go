package helmcat

import (
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"

	yaml3 "gopkg.in/yaml.v3"
)

// What can be filled in rather than typed: where a chart takes a storage
// class, an ingress, a size, a disk, a number of replicas. Charts name these
// differently, so they are found in the chart's own values, by the shapes the
// common charts use — Bitnami's, the one `helm create` writes, Grafana's and
// GitLab's. A chart that uses none of them is left to its values; its service
// can still be exposed from the Services page once it runs.

// Settings is what a chart's values let the portal fill in.
type Settings struct {
	// Storage are the paths that take a storage class, e.g.
	// global.defaultStorageClass or primary.persistence.storageClass.
	Storage []string `json:"storage"`
	// Ingresses are the ingress blocks the chart has.
	Ingresses []IngressSpot `json:"ingresses"`
	// Presets are Bitnami's resourcesPreset keys: a size of pod by name.
	Presets []string `json:"presets"`
	// Disks are the sizes of the chart's volumes: persistence.size.
	Disks []string `json:"disks"`
	// Replicas are how many pods a part of the chart runs.
	Replicas []string `json:"replicas"`
	// Defaults are the chart's own values at the paths above, as text.
	Defaults map[string]string `json:"defaults"`
	// Passwords are the chart's passwords, those sharing a default — most
	// likely one password told to two parts — grouped to be set together.
	Passwords []PasswordSpot `json:"passwords"`
	// GitSources are blocks that load content from a Git repository.
	GitSources []GitSpot `json:"gitSources"`
	// Components are the parts and features switched on or off.
	Components []Component `json:"components"`
	// Parts are the subcharts included, each by what it says it is.
	Parts map[string]string `json:"parts"`
}

// PasswordSpot is a password of the chart: one setting, or several that
// share their default and are set together.
type PasswordSpot struct {
	Paths []string `json:"paths"`
	// Default is what the chart ships; empty when it makes one up.
	Default string `json:"default"`
}

// GitSpot is a block loading content from Git — git-sync's gitSync, a
// chart's own git — with the keys it takes.
type GitSpot struct {
	Path       string   `json:"path"`
	RepoKey    string   `json:"repoKey"`
	BranchKeys []string `json:"branchKeys"`
	FolderKey  string   `json:"folderKey,omitempty"`
	// CredentialsKey names a Secret holding a login, when the block takes one.
	CredentialsKey string `json:"credentialsKey,omitempty"`
	// GitSync: the kubernetes git-sync sidecar, whose login is
	// GITSYNC_USERNAME and GITSYNC_PASSWORD (GIT_SYNC_… before v4).
	GitSync bool `json:"gitSync,omitempty"`
}

var (
	passwordKeyRE = regexp.MustCompile(`^(password|pass|[a-z][A-Za-z]*Password)$`)
	// Helpers' own passwords — metrics, replication — are not asked.
	helperPasswordRE = regexp.MustCompile(`(?i)^(metrics|replication|exporter|stats)`)
	gitBlockRE       = regexp.MustCompile(`(?i)^(gitSync|git|gitRepo|gitSource|repository)$`)
)

// gitSpot reads a block as a Git source, or not.
func gitSpot(path, key string, m map[string]any) (GitSpot, bool) {
	if !gitBlockRE.MatchString(key) || has(m, "tag") || has(m, "pullPolicy") {
		// An image of git-sync, not a repository to load.
		return GitSpot{}, false
	}
	g := GitSpot{Path: path, GitSync: strings.EqualFold(key, "gitSync")}
	for _, k := range []string{"repo", "repository", "url"} {
		if v, ok := m[k]; ok && !isMap(v) {
			g.RepoKey = k
			break
		}
	}
	if g.RepoKey == "" {
		return GitSpot{}, false
	}
	for _, k := range []string{"branch", "ref"} {
		if has(m, k) {
			g.BranchKeys = append(g.BranchKeys, k)
		}
	}
	for _, k := range []string{"subPath", "path", "dir"} {
		if v, ok := m[k]; ok && !isMap(v) {
			g.FolderKey = k
			break
		}
	}
	for _, k := range []string{"credentialsSecret", "existingSecret", "secretName", "sshKeySecret"} {
		if has(m, k) {
			g.CredentialsKey = k
			break
		}
	}
	return g, true
}

// IngressSpot is one ingress block in a chart's values.
type IngressSpot struct {
	// Path is where the block is: "ingress", or "server.ingress".
	Path string `json:"path"`
	// Shape is how the block names a host: "hostname" (Bitnami, Argo CD:
	// hostname, extraTls), "hosts" (helm create: hosts[].host with paths,
	// tls[]), "hostlist" (Grafana, Prometheus: hosts is a list of names,
	// with the path beside it), "namedhosts" (Airflow: hosts are
	// {name, tls}) or "domain" (GitLab: one domain under
	// global.hosts, each part of the chart a name in it).
	Shape string `json:"shape"`
	// ClassKey is the key the class goes under: ingressClassName or className.
	ClassKey string `json:"classKey"`
}

// maxDepth bounds the search: settings sit near the top of values, and
// examples buried in lists are not settings.
const maxDepth = 4

// Presets are the sizes Bitnami's resourcesPreset takes, smallest first.
var Presets = []string{"nano", "micro", "small", "medium", "large", "xlarge", "2xlarge"}

// Detect finds the settings in a chart's values.
func Detect(values map[string]any) Settings {
	return detect(values, true)
}

// detect finds the settings in values. ownGlobal: a global storage class is
// the chart's own, and covers its volumes; otherwise it is a subchart's, and
// the chart's own volumes are named one by one beside it.
func detect(values map[string]any, ownGlobal bool) Settings {
	s := Settings{Storage: []string{}, Ingresses: []IngressSpot{}, Presets: []string{}, Disks: []string{},
		Replicas: []string{}, Defaults: map[string]string{}, Components: []Component{}, Parts: map[string]string{}}
	var global []string
	if g, ok := values["global"].(map[string]any); ok {
		for _, k := range []string{"defaultStorageClass", "storageClass"} {
			if _, ok := g[k]; ok {
				// Bitnami's global applies to every volume of the chart and
				// its subcharts: one setting is enough.
				global = append(global, "global."+k)
				break
			}
		}
		// GitLab: every part gets a name in one domain.
		hosts, _ := g["hosts"].(map[string]any)
		ing, _ := g["ingress"].(map[string]any)
		if hosts != nil && ing != nil && has(hosts, "domain") {
			s.Ingresses = append(s.Ingresses, IngressSpot{Path: "global", Shape: "domain", ClassKey: "class"})
		}
	}
	// Each volume's own setting, used when there is no global one.
	var local []string
	var passwords []string
	var walk func(prefix string, m map[string]any, depth int)
	walk = func(prefix string, m map[string]any, depth int) {
		parent := prefix[strings.LastIndex(prefix, ".")+1:]
		for k, v := range m {
			path := k
			if prefix != "" {
				path = prefix + "." + k
			}
			if prefix == "" && k == "global" {
				continue
			}
			switch {
			case (k == "storageClass" || k == "storageClassName") && !isMap(v):
				local = append(local, path)
				continue
			case k == "resourcesPreset" && isString(v):
				// The workloads' own, not their helpers': a metrics
				// exporter or a backup job keeps the size it ships with.
				if !helperRE.MatchString(prefix) {
					s.Presets = append(s.Presets, path)
					s.Defaults[path] = text(v)
				}
				continue
			case k == "size" && isString(v) && (parent == "persistence" || parent == "persistentVolume"):
				s.Disks = append(s.Disks, path)
				s.Defaults[path] = text(v)
				continue
			case (k == "replicaCount" || k == "replicas") && isNumber(v) && depth <= 2:
				s.Replicas = append(s.Replicas, path)
				s.Defaults[path] = text(v)
				continue
			case passwordKeyRE.MatchString(k) && (v == nil || isString(v)) && !((k == "password" || k == "pass") && unusedLogin(m)) &&
				!helperPasswordRE.MatchString(k):
				passwords = append(passwords, path)
				s.Defaults[path] = text(v)
				continue
			}
			sub, ok := v.(map[string]any)
			if !ok {
				continue
			}
			if g, ok := gitSpot(path, k, sub); ok {
				// Off by default — that is the question.
				s.GitSources = append(s.GitSources, g)
				continue
			}
			if on, isSwitch := sub["enabled"].(bool); isSwitch && !on && k != "ingress" {
				// Switched off: its volumes and sizes are not asked about.
				continue
			}
			if k == "ingress" {
				if spot, ok := ingressSpot(path, sub); ok {
					s.Ingresses = append(s.Ingresses, spot)
					continue
				}
				// Airflow's: one block per part of the chart under ingress —
				// ingress.apiServer, ingress.web — and helpers' own beside.
				found := false
				for part, v := range sub {
					block, isMap := v.(map[string]any)
					if !isMap || ingressHelperRE.MatchString(part) {
						continue
					}
					if spot, ok := ingressSpot(path+"."+part, block); ok {
						s.Ingresses = append(s.Ingresses, spot)
						found = true
					}
				}
				if found {
					continue
				}
			}
			if depth < maxDepth {
				walk(path, sub, depth+1)
			}
		}
	}
	walk("", values, 1)
	switch {
	case len(global) > 0 && ownGlobal:
		s.Storage = global
	case len(global) > 0:
		s.Storage = append(global, local...)
	default:
		s.Storage = append(s.Storage, local...)
	}
	sort.Strings(s.Storage)
	sort.SliceStable(s.Ingresses, func(i, j int) bool {
		// GitLab's domain first, then the chart's own, then by name.
		if (s.Ingresses[i].Shape == "domain") != (s.Ingresses[j].Shape == "domain") {
			return s.Ingresses[i].Shape == "domain"
		}
		di, dj := strings.Count(s.Ingresses[i].Path, "."), strings.Count(s.Ingresses[j].Path, ".")
		if di != dj {
			return di < dj
		}
		return s.Ingresses[i].Path < s.Ingresses[j].Path
	})
	s.Storage = shallowFirst(s.Storage, 12)
	s.Presets = shallowFirst(s.Presets, 8)
	s.Disks = shallowFirst(s.Disks, 8)
	s.Replicas = shallowFirst(s.Replicas, 6)
	s.Passwords = groupPasswords(shallowFirst(passwords, 10), s.Defaults)
	sort.SliceStable(s.GitSources, func(i, j int) bool {
		return strings.Count(s.GitSources[i].Path, ".") < strings.Count(s.GitSources[j].Path, ".") ||
			(strings.Count(s.GitSources[i].Path, ".") == strings.Count(s.GitSources[j].Path, ".") && s.GitSources[i].Path < s.GitSources[j].Path)
	})
	return s
}

// unusedLogin: a password beside a user name left empty — a login the chart
// creates only when asked, as Bitnami's auth.username and auth.password.
func unusedLogin(m map[string]any) bool {
	for _, k := range []string{"username", "user"} {
		if v, ok := m[k]; ok && (v == nil || v == "") {
			return true
		}
	}
	return false
}

// groupPasswords puts together the passwords that ship the same value: a
// chart telling its database's password to the part that connects to it
// says it twice, and the two must change together.
func groupPasswords(paths []string, defaults map[string]string) []PasswordSpot {
	out := []PasswordSpot{}
	at := map[string]int{}
	for _, p := range paths {
		d := defaults[p]
		if d != "" {
			if i, ok := at[d]; ok {
				out[i].Paths = append(out[i].Paths, p)
				continue
			}
			at[d] = len(out)
		} else {
			d = ""
		}
		out = append(out, PasswordSpot{Paths: []string{p}, Default: d})
	}
	return out
}

// ingressHelperRE names ingress blocks for a chart's metrics, not its users.
var ingressHelperRE = regexp.MustCompile(`(?i)(statsd|pgbouncer|metrics|exporter|prometheus)`)

// helperRE names the parts of a chart that only help its workloads run.
var helperRE = regexp.MustCompile(`(?i)(metrics|exporter|volumePermissions|sysctl|init|job|backup|sidecar|tls|auth)`)

// shallowFirst orders paths the chart's own first, and keeps at most n.
func shallowFirst(paths []string, n int) []string {
	sort.SliceStable(paths, func(i, j int) bool {
		di, dj := strings.Count(paths[i], "."), strings.Count(paths[j], ".")
		if di != dj {
			return di < dj
		}
		return paths[i] < paths[j]
	})
	if len(paths) > n {
		paths = paths[:n]
	}
	return paths
}

func isMap(v any) bool    { _, ok := v.(map[string]any); return ok }
func isString(v any) bool { _, ok := v.(string); return ok }

func isNumber(v any) bool {
	switch v.(type) {
	case int, int64, float64:
		return true
	}
	return false
}

func text(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	default:
		return fmt.Sprint(t)
	}
}

func ingressSpot(path string, m map[string]any) (IngressSpot, bool) {
	if _, ok := m["enabled"]; !ok {
		return IngressSpot{}, false
	}
	spot := IngressSpot{Path: path}
	_, tlsBlock := m["tls"].(map[string]any)
	switch {
	case has(m, "host") && has(m, "hosts") && tlsBlock:
		// Airflow: hosts are {name, tls: {enabled, secretName}}, the class
		// beside them.
		spot.Shape, spot.ClassKey = "namedhosts", "ingressClassName"
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
	// Preset is a resourcesPreset for every path in PresetPaths.
	Preset      string   `json:"preset"`
	PresetPaths []string `json:"presetPaths"`
	// Disk is a volume size, e.g. 20Gi, for every path in DiskPaths.
	Disk      string   `json:"disk"`
	DiskPaths []string `json:"diskPaths"`
	// DiskSizes are sizes volume by volume; an empty one leaves the chart's.
	DiskSizes map[string]string `json:"diskSizes"`
	// Replicas are pod counts by path.
	Replicas map[string]int `json:"replicas"`
	// Set are values typed for keys the chart asked for, each written as
	// a YAML scalar would read: 3 is a number, true a yes, the rest text.
	Set map[string]string `json:"set"`
	// Extra are values typed for settings the chart takes but does not ask
	// for — a user name beside a host it does ask for.
	Extra map[string]string `json:"extra"`
	// Passwords are passwords by path, written as text.
	Passwords map[string]string `json:"passwords"`
	// Git loads content from a repository.
	Git *GitFill `json:"git"`
}

// GitFill is a Git source to switch on.
type GitFill struct {
	GitSpot
	Repo        string `json:"repo"`
	Branch      string `json:"branch"`
	Folder      string `json:"folder"`
	Credentials string `json:"credentials"`
}

// IngressFill is an ingress to switch on in one block.
type IngressFill struct {
	IngressSpot
	// Host is the address; for the "domain" shape, the domain.
	Host  string `json:"host"`
	Class string `json:"class"`
	// TLS is an existing secret holding the certificate; empty for none.
	TLS string `json:"tls"`
}

// assign is one key a fill writes, and the note written beside it.
type assign struct {
	v    any
	note string
}

var (
	diskRE = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?(Mi|Gi|Ti)$`)
	pathRE = regexp.MustCompile(`^[A-Za-z0-9_-]+(\.[A-Za-z0-9_-]+)*$`)
)

// The notes written beside what the form fills in, so the values say where
// each line came from.
const (
	noteStorage  = "storage class, from the install form"
	noteIngress  = "opens it in a browser, from the install form"
	noteSize     = "size, from the install form"
	noteDisk     = "disk size, from the install form"
	noteReplicas = "replicas, from the install form"
	noteRequired = "the chart asks for this"
	noteExtra    = "from the install form"
	notePassword = "password, from the install form"
	noteGit      = "loads from Git, from the install form"
)

// assignments are the keys a fill sets, each with its value.
func (f Fill) assignments() (map[string]assign, error) {
	out := map[string]assign{}
	if f.StorageClass != "" {
		for _, p := range f.StoragePaths {
			out[p] = assign{f.StorageClass, noteStorage}
		}
	}
	if f.Preset != "" {
		if !contains(Presets, f.Preset) {
			return nil, fmt.Errorf("%q is not a size the chart knows", f.Preset)
		}
		for _, p := range f.PresetPaths {
			out[p] = assign{f.Preset, noteSize}
		}
	}
	if f.Disk != "" {
		if !diskRE.MatchString(f.Disk) {
			return nil, fmt.Errorf("a disk size is a number with Mi, Gi or Ti, like 20Gi")
		}
		for _, p := range f.DiskPaths {
			out[p] = assign{f.Disk, noteDisk}
		}
	}
	for p, size := range f.DiskSizes {
		if size = strings.TrimSpace(size); size == "" {
			continue
		}
		if !diskRE.MatchString(size) {
			return nil, fmt.Errorf("a disk size is a number with Mi, Gi or Ti, like 20Gi")
		}
		out[p] = assign{size, noteDisk}
	}
	for p, n := range f.Replicas {
		if n < 0 || n > 50 {
			return nil, fmt.Errorf("%d replicas is not something to install from here", n)
		}
		out[p] = assign{n, noteReplicas}
	}
	for p, raw := range f.Set {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		out[p] = assign{scalar(raw), noteRequired}
	}
	for p, pw := range f.Passwords {
		if pw != "" {
			out[p] = assign{pw, notePassword}
		}
	}
	if g := f.Git; g != nil {
		if strings.TrimSpace(g.Repo) == "" {
			return nil, fmt.Errorf("the repository needs an address")
		}
		out[g.Path+".enabled"] = assign{true, noteGit}
		out[g.Path+"."+g.RepoKey] = assign{strings.TrimSpace(g.Repo), ""}
		for _, k := range g.BranchKeys {
			if b := strings.TrimSpace(g.Branch); b != "" {
				out[g.Path+"."+k] = assign{b, ""}
			}
		}
		if g.FolderKey != "" {
			// Written even when empty: the chart's own example folder is not
			// the repository's.
			out[g.Path+"."+g.FolderKey] = assign{strings.Trim(strings.TrimSpace(g.Folder), "/"), ""}
		}
		if g.CredentialsKey != "" && g.Credentials != "" {
			out[g.Path+"."+g.CredentialsKey] = assign{g.Credentials, ""}
		}
	}
	for p, raw := range f.Extra {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		if _, asked := out[p]; !asked {
			out[p] = assign{scalar(raw), noteExtra}
		}
	}
	for p := range out {
		if !pathRE.MatchString(p) {
			return nil, fmt.Errorf("%q is not a path", p)
		}
	}
	if f.Ingress != nil {
		ing, err := f.Ingress.assignments()
		if err != nil {
			return nil, err
		}
		for p, a := range ing {
			out[p] = a
		}
	}
	return out, nil
}

func (in *IngressFill) assignments() (map[string]assign, error) {
	out := map[string]assign{}
	if in.Host == "" {
		return nil, fmt.Errorf("the ingress needs a host")
	}
	if in.Shape == "domain" {
		out["global.hosts.domain"] = assign{in.Host, noteIngress}
		if in.Class != "" {
			out["global.ingress.class"] = assign{in.Class, ""}
		}
		if in.TLS != "" {
			out["global.ingress.configureCertmanager"] = assign{false, ""}
			out["global.ingress.tls.secretName"] = assign{in.TLS, ""}
		}
		return out, nil
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
	case "namedhosts":
		host := map[string]any{"name": in.Host}
		if in.TLS != "" {
			host["tls"] = map[string]any{"enabled": true, "secretName": in.TLS}
		}
		block["hosts"] = []any{host}
	default:
		return nil, fmt.Errorf("unknown ingress shape %q", in.Shape)
	}
	for k, v := range block {
		note := ""
		if k == "enabled" {
			note = noteIngress
		}
		out[in.Path+"."+k] = assign{v, note}
	}
	return out, nil
}

// scalar reads typed text the way YAML would: numbers and yes/no as such,
// anything else — or anything that would read as a list or a map — as text.
func scalar(raw string) any {
	var v any
	if err := yaml3.Unmarshal([]byte(raw), &v); err != nil {
		return raw
	}
	switch v.(type) {
	case int, float64, bool:
		return v
	}
	return raw
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

// ApplyFill brings values written as YAML from what an earlier fill, prev,
// wrote to what f writes now — the install form sends each change as it is
// made. Keys prev wrote and f does not are taken out again. A key whose value
// is no longer what prev wrote was changed by hand in the editor: it is left
// as it is, and named in kept. Everything else in the values, comments
// included, stays as it was written; what the fill writes gets a note.
func ApplyFill(text string, f, prev Fill) (out string, kept []string, err error) {
	want, err := f.assignments()
	if err != nil {
		return "", nil, err
	}
	had, err := prev.assignments()
	if err != nil {
		had = map[string]assign{}
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
		return cur == nil || sameValue(cur, old.v)
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
		a, keep := want[p]
		if !ours(p) {
			if keep || !sameValue(lookup(root, p), had[p].v) {
				kept = append(kept, p)
			}
			continue
		}
		if !keep {
			remove(root, strings.Split(p, "."))
			continue
		}
		if cur := lookup(root, p); cur != nil && sameValue(cur, a.v) {
			continue
		}
		if err := setNode(root, p, a.v, a.note); err != nil {
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

// setNode sets a.b.c to v, making the maps on the way, with note beside a
// value that fits on one line.
func setNode(m *yaml3.Node, path string, v any, note string) error {
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
			if note != "" && val.Kind == yaml3.ScalarNode {
				val.LineComment = "# " + note
			}
			if next != nil {
				// Keep what was written beside the value.
				if next.LineComment != "" {
					val.LineComment = next.LineComment
				}
				val.HeadComment, val.FootComment = next.HeadComment, next.FootComment
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
