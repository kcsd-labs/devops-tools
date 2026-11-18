package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"helm.sh/helm/v3/pkg/chart"
	"sigs.k8s.io/yaml"

	"devops-tools/internal/audit"
	"devops-tools/internal/auth"
	"devops-tools/internal/helmcat"
	"devops-tools/internal/helmreq"
	"devops-tools/internal/kube"
)

// The catalog: charts from Artifact Hub, installed into a namespace with an
// administrator's approval — or straight away by an administrator.
//
// Approving is the global user-manage: the people who decide who may do what
// are the ones who decide what runs. Everything a request carries is checked
// again when it is approved, against a fresh copy of the chart that must be
// the same archive the requester saw.

// catalog is what the catalog endpoints need. Nil when it is switched off.
type catalog struct {
	hub      *helmcat.Hub
	fetcher  *helmcat.Fetcher
	requests helmreq.Store
	// planner and installer are the cluster's own, but for tests.
	planner   func(ctx context.Context, ns, name string, ch *chart.Chart, vals map[string]any, upgrade bool) (*kube.ReleasePlan, error)
	installer func(ctx context.Context, r *helmreq.Request, ch *chart.Chart, vals map[string]any) error
	images    func(ctx context.Context, images []string) []helmcat.ImageState
	// fetch reads one version of a chart: where it is and the archive.
	fetch func(ctx context.Context, repo, name, version string) (*helmcat.Located, *helmcat.Archive, error)
	// current reads a release installed from the catalog; trouble says why
	// its pods are not up; releases lists a namespace's releases, read with
	// the portal's account; namespaces lists the cluster's.
	current    func(ns, name string) (*kube.CatalogRelease, error)
	trouble    func(ctx context.Context, ns, name string) string
	releases   func(ns string) ([]kube.HelmRelease, error)
	namespaces func(ctx context.Context) ([]string, error)
	// fromCatalog lists every release installed from the catalog, in every
	// namespace.
	fromCatalog func() ([]kube.HelmRelease, error)
	// hints is what the cluster offers for a namespace: storage classes,
	// ingress class, domain, certificates.
	hints func(ctx context.Context, ns string) *kube.ClusterHints
	// nsExists says whether a namespace is there; a new one is asked for.
	nsExists func(ctx context.Context, ns string) (bool, error)
	// templates are the starting values administrators keep per chart.
	templates helmreq.Templates
	// secretExists says whether a namespace has a Secret by that name.
	secretExists func(ctx context.Context, ns, name string) (bool, error)
	// progress is how far a release's pods have come.
	progress func(ctx context.Context, ns, name string) *kube.InstallProgress
	// running are the installs under way here.
	running installs
}

// WithCatalog switches the catalog on.
func (s *Server) WithCatalog(hub *helmcat.Hub, fetcher *helmcat.Fetcher, requests helmreq.Store) *Server {
	c := &catalog{hub: hub, fetcher: fetcher, requests: requests, images: helmcat.CheckImages}
	c.planner = s.kube.PlanRelease
	c.installer = s.runInstall
	c.current = s.kube.GetCatalogRelease
	c.trouble = s.kube.ReleaseTrouble
	c.releases = func(ns string) ([]kube.HelmRelease, error) { return s.kube.ListReleases("", ns) }
	c.fromCatalog = s.kube.ListCatalogReleases
	c.hints = s.kube.GetClusterHints
	c.nsExists = s.kube.NamespaceExists
	c.secretExists = s.kube.SecretExists
	c.progress = s.kube.ReleaseProgress
	c.templates = helmreq.NewMemoryTemplates()
	c.namespaces = func(ctx context.Context) ([]string, error) {
		return s.kube.ListNamespaces(ctx, "", s.cfg.Cluster.HiddenNamespaces)
	}
	c.fetch = func(ctx context.Context, repo, name, version string) (*helmcat.Located, *helmcat.Archive, error) {
		loc, err := hub.Locate(ctx, repo, name, version)
		if err != nil {
			return nil, nil, err
		}
		a, err := fetcher.Fetch(ctx, loc.ContentURL)
		if err != nil {
			return nil, nil, err
		}
		return loc, a, nil
	}
	s.catalog = c
	return s
}

// WithCatalogTemplates keeps administrators' starting values somewhere that
// outlives the process.
func (s *Server) WithCatalogTemplates(t helmreq.Templates) *Server {
	if s.catalog != nil && t != nil {
		s.catalog.templates = t
	}
	return s
}

func (s *Server) catalogRoutes(r chi.Router, base string) {
	if s.catalog == nil {
		return
	}
	// The Helm Packages page belongs to no namespace: the catalog is one list,
	// and what was installed or asked for is shown across every namespace the
	// caller may ask in. The namespace is chosen when installing. Open to
	// whoever may ask for an install or an upgrade somewhere, whether or not
	// they hold helm-list.
	r.Get("/helm/namespaces", s.requirePackages(s.handleCatalogNamespaces))
	r.Get("/helm/access", s.requirePackages(s.handleCatalogAccess))
	r.Get("/helm/catalog", s.requirePackages(s.handleCatalogSearch))
	r.Get("/helm/catalog/{repo}/{chart}", s.requirePackages(s.handleCatalogChart))
	r.Get("/helm/catalog-releases", s.requirePackages(s.handleCatalogReleases))
	r.Get("/helm/my-requests", s.requirePackages(s.handleMyRequests))
	r.Post("/helm/values/fill", s.requirePackages(s.handleFillValues))

	// Acting in one namespace is checked against that namespace.
	r.Get(base+"/helm/releases/{name}/catalog", s.requireOp(OpHelmUpgrade, s.handleCatalogRelease))
	r.Post(base+"/helm/check", s.handleCatalogCheck)
	r.Post(base+"/helm/requests", s.handleCreateRequest)
	// Withdrawing is the requester's, or an administrator's — a request for a
	// new namespace is one its requester holds nothing in yet.
	r.Delete(base+"/helm/requests/{id}", s.requirePackages(s.handleWithdrawRequest))
	r.Post(base+"/helm/requests/{id}/stop", s.requirePackages(s.handleStopInstall))

	r.Get("/helm/requests", s.requireGlobalOp(OpUserManage, s.handleAllRequests))
	r.Get("/helm/requests/count", s.requireGlobalOp(OpUserManage, s.handleRequestCount))
	r.Post("/helm/requests/{id}/check", s.requireGlobalOp(OpUserManage, s.handleCheckRequest))
	r.Post("/helm/requests/{id}/approve", s.requireGlobalOp(OpUserManage, s.handleApproveRequest))
	r.Post("/helm/requests/{id}/reject", s.requireGlobalOp(OpUserManage, s.handleRejectRequest))
	r.Get("/helm/requests/{id}/grants", s.requireGlobalOp(OpUserManage, s.handleRequestGrants))

	r.Put("/helm/templates/{repo}/{chart}", s.requireGlobalOp(OpUserManage, s.handleSaveTemplate))
	r.Delete("/helm/templates/{repo}/{chart}", s.requireGlobalOp(OpUserManage, s.handleDeleteTemplate))
}

// requireAnyOp is requireOp for an endpoint either of two grants opens.
func (s *Server) requireAnyOp(ops []string, handler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, ok := auth.FromContext(r.Context())
		if !ok {
			http.Error(w, "unauthenticated", http.StatusUnauthorized)
			return
		}
		ns := chi.URLParam(r, "namespace")
		for _, op := range ops {
			if s.authz.Allowed(user.Roles, ns, op) {
				handler(w, r)
				return
			}
		}
		s.audit.Log(audit.Entry{User: user.Username, Environment: s.cfg.Environment, Namespace: ns,
			Operation: ops[0], Allowed: false})
		http.Error(w, "forbidden", http.StatusForbidden)
	}
}

func (s *Server) isApprover(roles []string) bool { return s.authz.AllowedGlobal(roles, OpUserManage) }

// mayAskAnywhere reports whether these roles may ask for an install or an
// upgrade in some namespace: what opens the Helm Packages page.
func (s *Server) mayAskAnywhere(roles []string) bool {
	return len(s.authz.NamespacesForOp(roles, OpHelmInstall)) > 0 ||
		len(s.authz.NamespacesForOp(roles, OpHelmUpgrade)) > 0
}

// requirePackages lets through whoever may ask for an install or an upgrade
// somewhere.
func (s *Server) requirePackages(handler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, ok := auth.FromContext(r.Context())
		if !ok {
			http.Error(w, "unauthenticated", http.StatusUnauthorized)
			return
		}
		if !s.mayAskAnywhere(user.Roles) {
			s.audit.Log(audit.Entry{User: user.Username, Environment: s.cfg.Environment, Namespace: "-",
				Operation: OpHelmInstall, Allowed: false})
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		handler(w, r)
	}
}

func (s *Server) handleCatalogAccess(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.FromContext(r.Context())
	writeJSON(w, http.StatusOK, map[string]bool{
		"install": len(s.authz.NamespacesForOp(user.Roles, OpHelmInstall)) > 0,
		"upgrade": len(s.authz.NamespacesForOp(user.Roles, OpHelmUpgrade)) > 0,
		// An administrator's install needs no request.
		"direct": s.isApprover(user.Roles),
		// A new namespace can be asked for by whoever may install somewhere.
		"newNamespace": s.mayInstallSomewhere(user.Roles),
	})
}

// mayInstallSomewhere reports whether these roles may ask for an install in
// some namespace — and so for one into a new namespace.
func (s *Server) mayInstallSomewhere(roles []string) bool {
	return len(s.authz.NamespacesForOp(roles, OpHelmInstall)) > 0
}

var namespaceRE = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`)

var (
	secretNameRE = regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]{0,251}[a-z0-9])?$`)
	secretKeyRE  = regexp.MustCompile(`^[-._a-zA-Z0-9]{1,253}$`)
)

// checkNewSecrets checks the Secrets a request would create: names and keys
// Kubernetes takes, a sane size, and none already in the namespace — that
// one is picked by its name instead.
func (s *Server) checkNewSecrets(ctx context.Context, ns string, secrets []helmreq.NewSecret) error {
	if len(secrets) > 5 {
		return fmt.Errorf("at most five Secrets are created with a release")
	}
	seen := map[string]bool{}
	size := 0
	for _, sec := range secrets {
		if !secretNameRE.MatchString(sec.Name) {
			return fmt.Errorf("%q is not a Secret name: lower-case letters, digits, dashes and dots", sec.Name)
		}
		if seen[sec.Name] {
			return fmt.Errorf("the Secret %s is asked for twice", sec.Name)
		}
		seen[sec.Name] = true
		if len(sec.Data) == 0 || len(sec.Data) > 30 {
			return fmt.Errorf("the Secret %s needs between one and thirty keys", sec.Name)
		}
		for k, v := range sec.Data {
			if !secretKeyRE.MatchString(k) {
				return fmt.Errorf("%q is not a key a Secret takes: letters, digits, dashes, dots and underscores", k)
			}
			size += len(k) + len(v)
		}
		if exists, err := s.catalog.secretExists(ctx, ns, sec.Name); err == nil && exists {
			return fmt.Errorf("%s already has a Secret named %s — pick it by its name instead", ns, sec.Name)
		}
	}
	if size > 256<<10 {
		return fmt.Errorf("the Secrets are too large: 256 KiB at most")
	}
	return nil
}

// newNamespaceProblem says why ns cannot be a new namespace, or "".
func (s *Server) newNamespaceProblem(ctx context.Context, ns string) string {
	if !namespaceRE.MatchString(ns) {
		return "a namespace name is lower-case letters, digits and dashes, at most 63 long"
	}
	if s.authz.Reserved(ns) {
		return ns + " is reserved for the cluster or the portal"
	}
	exists, err := s.catalog.nsExists(ctx, ns)
	if err != nil {
		return "could not check the namespace: " + err.Error()
	}
	if exists {
		return ns + " already exists — pick it from the list, or ask for access to it"
	}
	return ""
}

// mayAskFor reports whether these roles may ask for an install or an upgrade
// in ns — what puts a namespace on the Helm Packages page.
func (s *Server) mayAskFor(roles []string, ns string) bool {
	return s.authz.Allowed(roles, ns, OpHelmInstall) || s.authz.Allowed(roles, ns, OpHelmUpgrade)
}

// handleCatalogNamespaces lists the namespaces a chart may be installed into
// by the caller — what the install dialog offers. Those holding helm-install;
// never the closed ones, whatever a role says.
func (s *Server) handleCatalogNamespaces(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.FromContext(r.Context())
	candidates := s.authz.NamespacesForOp(user.Roles, OpHelmInstall)
	if len(candidates) == 1 && candidates[0] == "*" {
		all, err := s.catalog.namespaces(r.Context())
		if err != nil {
			http.Error(w, "could not list the namespaces: "+err.Error(), http.StatusBadGateway)
			return
		}
		candidates = unionSorted(all, s.authz.NamedNamespaces(user.Roles, OpHelmInstall))
	}
	out := []string{}
	for _, ns := range candidates {
		if ns != "*" && s.authz.Allowed(user.Roles, ns, OpHelmInstall) {
			out = append(out, ns)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"namespaces": out})
}

// catalogReleaseRow is a release from the catalog, with what the caller may
// do with it.
type catalogReleaseRow struct {
	kube.HelmRelease
	MayUpgrade bool `json:"mayUpgrade"`
	// MayList: the Helm releases page, with the history, is open to them.
	MayList bool `json:"mayList"`
	// MayUninstall: they may remove it.
	MayUninstall bool `json:"mayUninstall"`
}

// handleCatalogReleases lists the releases installed from the catalog in the
// namespaces the caller may ask in.
func (s *Server) handleCatalogReleases(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.FromContext(r.Context())
	rels, err := s.catalog.fromCatalog()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	out := []catalogReleaseRow{}
	for _, rel := range rels {
		if rel.Catalog == nil || !s.mayAskFor(user.Roles, rel.Namespace) {
			continue
		}
		out = append(out, catalogReleaseRow{
			HelmRelease:  rel,
			MayUpgrade:   s.authz.Allowed(user.Roles, rel.Namespace, OpHelmUpgrade),
			MayList:      s.authz.Allowed(user.Roles, rel.Namespace, OpHelmList),
			MayUninstall: s.authz.Allowed(user.Roles, rel.Namespace, OpHelmUninstall),
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Namespace != out[j].Namespace {
			return out[i].Namespace < out[j].Namespace
		}
		return out[i].Name < out[j].Name
	})
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleCatalogSearch(w http.ResponseWriter, r *http.Request) {
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	page, err := s.catalog.hub.Search(r.Context(), r.URL.Query().Get("q"), offset)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, page)
}

// chartInfo is one version of a chart, ready to fill in.
type chartInfo struct {
	*helmcat.Located
	Digest   string `json:"digest"`
	Defaults string `json:"defaults"`
	Schema   bool   `json:"schema"`
	// SuggestedName is a release name free in the namespace.
	SuggestedName string `json:"suggestedName"`
	// Settings are what the chart's values let the portal fill in, and
	// Cluster what it fills them with — the latter only with a namespace.
	Settings helmcat.Settings   `json:"settings"`
	Cluster  *kube.ClusterHints `json:"cluster,omitempty"`
	// Docs are what the chart says about each of its keys.
	Docs map[string]string `json:"docs"`
	// Examples are settings the chart shows only commented out, with the
	// value each shows: optional ones, offered beside the ones it asks for.
	Examples map[string]string `json:"examples"`
	// Template is what administrators keep as the chart's starting values.
	Template *helmreq.Template `json:"template,omitempty"`
	// NamespaceProblem says why a new namespace by that name will not do.
	NamespaceProblem string `json:"namespaceProblem,omitempty"`
}

func (s *Server) handleCatalogChart(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.FromContext(r.Context())
	ns := r.URL.Query().Get("namespace")
	loc, arc, err := s.catalog.fetch(r.Context(), chi.URLParam(r, "repo"), chi.URLParam(r, "chart"), r.URL.Query().Get("version"))
	if err != nil {
		catalogError(w, err)
		return
	}
	info := chartInfo{Located: loc, Digest: arc.Digest, Schema: len(arc.Chart.Schema) > 0,
		Settings: helmcat.DetectChart(arc.Chart), Docs: helmcat.ChartDocs(arc.Chart), Examples: helmcat.ChartExamples(arc.Chart)}
	for _, f := range arc.Chart.Raw {
		if f.Name == "values.yaml" {
			info.Defaults = string(f.Data)
		}
	}
	if t, err := s.catalog.templates.Get(r.Context(), loc.Repo, loc.Name); err == nil {
		info.Template = t
	}
	isNew := r.URL.Query().Get("new") == "1"
	if isNew && ns != "" && s.mayInstallSomewhere(user.Roles) {
		// A namespace to be: nothing in it to probe, the cluster's own hints.
		info.SuggestedName = loc.Name
		if info.NamespaceProblem = s.newNamespaceProblem(r.Context(), ns); info.NamespaceProblem == "" {
			info.Cluster = s.catalog.hints(r.Context(), ns)
		}
		writeJSON(w, http.StatusOK, info)
		return
	}
	// A free name only where the caller may look: the names in a namespace
	// are not theirs to probe otherwise.
	if ns != "" && s.mayAskFor(user.Roles, ns) {
		info.SuggestedName = s.freeReleaseName(ns, loc.Name)
		info.Cluster = s.catalog.hints(r.Context(), ns)
	} else {
		info.SuggestedName = loc.Name
	}
	writeJSON(w, http.StatusOK, info)
}

// freeReleaseName is name, or name-2, name-3… — the first not taken in ns.
func (s *Server) freeReleaseName(ns, name string) string {
	rels, err := s.catalog.releases(ns)
	if err != nil {
		return name
	}
	taken := map[string]bool{}
	for _, r := range rels {
		taken[r.Name] = true
	}
	for i := 1; ; i++ {
		n := name
		if i > 1 {
			n = name + "-" + strconv.Itoa(i)
		}
		if !taken[n] {
			return n
		}
	}
}

func (s *Server) handleCatalogRelease(w http.ResponseWriter, r *http.Request) {
	ns, name := chi.URLParam(r, "namespace"), chi.URLParam(r, "name")
	rel, err := s.catalog.current(ns, name)
	if errors.Is(err, kube.ErrNotFromCatalog) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, rel)
}

// handleFillValues writes what the install form fills from the cluster into
// the values — a storage class, an ingress — replacing what it wrote before
// (previous). Pure text in and out; nothing is read or changed in the cluster.
func (s *Server) handleFillValues(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Values string `json:"values"`
		helmcat.Fill
		Previous helmcat.Fill `json:"previous"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 512<<10)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "the request does not read: " + err.Error()})
		return
	}
	out, kept, err := helmcat.ApplyFill(body.Values, body.Fill, body.Previous)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if kept == nil {
		kept = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"values": out, "kept": kept})
}

// catalogAsk is what a check or a request carries.
type catalogAsk struct {
	Kind    string `json:"kind"`
	Release string `json:"release"`
	Repo    string `json:"repo"`
	Chart   string `json:"chart"`
	Version string `json:"version"`
	Values  string `json:"values"`
	// NewNamespace asks for the namespace to be created with the release.
	NewNamespace bool `json:"newNamespace"`
	// Known are the required values the form already has fields for.
	Known []string `json:"known,omitempty"`
	// Help asks an administrator to finish values that do not pass yet,
	// saying what is missing.
	Help string `json:"help,omitempty"`
	// Secrets are created in the namespace before the install.
	Secrets []helmreq.NewSecret `json:"secrets,omitempty"`
}

// checkResult is a plan, with what the registries said about its images.
type checkResult struct {
	*kube.ReleasePlan
	OK          bool                 `json:"ok"`
	Digest      string               `json:"digest"`
	ImageStates []helmcat.ImageState `json:"imageStates"`
	FromVersion string               `json:"fromVersion,omitempty"`
}

var releaseNameRE = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,51}[a-z0-9])?$`)

// readAsk reads and checks a request body, and answers the caller itself when
// it is not one to act on.
func (s *Server) readAsk(w http.ResponseWriter, r *http.Request) (*catalogAsk, bool) {
	user, _ := auth.FromContext(r.Context())
	ns := chi.URLParam(r, "namespace")
	var ask catalogAsk
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 512<<10)).Decode(&ask); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "the request does not read: " + err.Error()})
		return nil, false
	}
	op := OpHelmInstall
	switch ask.Kind {
	case helmreq.KindInstall:
	case helmreq.KindUpgrade:
		op = OpHelmUpgrade
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "kind is install or upgrade"})
		return nil, false
	}
	if ask.NewNamespace {
		// Nobody holds anything in a namespace that is not there yet: whoever
		// may install somewhere may ask for one, and an administrator decides.
		if ask.Kind != helmreq.KindInstall || !s.mayInstallSomewhere(user.Roles) {
			s.audit.Log(audit.Entry{User: user.Username, Environment: s.cfg.Environment, Namespace: ns,
				Operation: op, Target: ask.Release, Allowed: false})
			http.Error(w, "forbidden", http.StatusForbidden)
			return nil, false
		}
		if problem := s.newNamespaceProblem(r.Context(), ns); problem != "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": problem})
			return nil, false
		}
	} else if !s.authz.Allowed(user.Roles, ns, op) {
		s.audit.Log(audit.Entry{User: user.Username, Environment: s.cfg.Environment, Namespace: ns,
			Operation: op, Target: ask.Release, Allowed: false})
		http.Error(w, "forbidden", http.StatusForbidden)
		return nil, false
	}
	if !releaseNameRE.MatchString(ask.Release) {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "a release name is lower-case letters, digits and dashes, at most 53 long"})
		return nil, false
	}
	return &ask, true
}

// parseValues reads values written as YAML.
func parseValues(text string) (map[string]any, error) {
	vals := map[string]any{}
	if strings.TrimSpace(text) == "" {
		return vals, nil
	}
	if err := yaml.Unmarshal([]byte(text), &vals); err != nil {
		return nil, fmt.Errorf("the values are not YAML: %w", err)
	}
	if vals == nil {
		vals = map[string]any{}
	}
	return vals, nil
}

// check plans an ask: fetches the chart, renders it, and asks the registries
// about its images. wantDigest, when set, is the archive that must be fetched.
func (s *Server) check(ctx context.Context, ns string, ask *catalogAsk, wantDigest string) (*checkResult, *chart.Chart, map[string]any, error) {
	vals, err := parseValues(ask.Values)
	if err != nil {
		return nil, nil, nil, askError{err}
	}
	if err := s.checkNewSecrets(ctx, ns, ask.Secrets); err != nil {
		return nil, nil, nil, askError{err}
	}
	start := time.Now()
	var planned *kube.ReleasePlan
	var fetchTook, imagesTook time.Duration
	defer func() {
		// A slow check says where the time went: the chart's download, its
		// rendering, the search for what it needs, the registries.
		total := time.Since(start)
		if total < 2*time.Second {
			return
		}
		attrs := []any{"chart", ask.Repo + "/" + ask.Chart, "version", ask.Version, "namespace", ns,
			"total", total.Round(time.Millisecond).String(), "fetch", fetchTook.Round(time.Millisecond).String(),
			"images", imagesTook.Round(time.Millisecond).String()}
		if planned != nil {
			for _, t := range planned.Timings {
				attrs = append(attrs, t.Part, t.Took.Round(time.Millisecond).String())
			}
		}
		if ctx.Err() != nil {
			attrs = append(attrs, "abandoned", true)
		}
		slog.Info("slow catalog check", attrs...)
	}()
	_, arc, err := s.catalog.fetch(ctx, ask.Repo, ask.Chart, ask.Version)
	fetchTook = time.Since(start)
	if err != nil {
		return nil, nil, nil, err
	}
	if wantDigest != "" && arc.Digest != wantDigest {
		return nil, nil, nil, askError{fmt.Errorf("the chart %s %s is no longer the archive that was requested — its author replaced it; ask again",
			ask.Chart, ask.Version)}
	}
	upgrade := ask.Kind == helmreq.KindUpgrade
	res := &checkResult{Digest: arc.Digest, ImageStates: []helmcat.ImageState{}}
	if upgrade {
		cur, err := s.catalog.current(ns, ask.Release)
		if errors.Is(err, kube.ErrNotFromCatalog) {
			return nil, nil, nil, askError{err}
		}
		if err != nil {
			return nil, nil, nil, err
		}
		if cur.Origin.Repo != ask.Repo || cur.Origin.Chart != ask.Chart {
			return nil, nil, nil, askError{fmt.Errorf("%s was installed from %s/%s, not %s/%s",
				ask.Release, cur.Origin.Repo, cur.Origin.Chart, ask.Repo, ask.Chart)}
		}
		res.FromVersion = cur.Version
	}
	plan, err := s.catalog.planner(kube.WithKnownRequired(ctx, ask.Known), ns, ask.Release, arc.Chart, vals, upgrade)
	if err != nil {
		return nil, nil, nil, err
	}
	planned = plan
	res.ReleasePlan = plan
	if plan.OK() && len(plan.Images) > 0 {
		began := time.Now()
		res.ImageStates = s.catalog.images(ctx, plan.Images)
		imagesTook = time.Since(began)
		for _, st := range res.ImageStates {
			switch {
			case st.Missing:
				plan.Problems = append(plan.Problems, kube.PlanProblem{Level: "warn",
					Text: "image " + st.Image + " was not found in its registry — unless the nodes pull it from a mirror, the pods will not start"})
			case st.Unknown != "":
				plan.Problems = append(plan.Problems, kube.PlanProblem{Level: "warn",
					Text: "image " + st.Image + " could not be checked: " + st.Unknown})
			}
		}
	}
	res.OK = plan.OK()
	return res, arc.Chart, vals, nil
}

func (s *Server) handleCatalogCheck(w http.ResponseWriter, r *http.Request) {
	ask, ok := s.readAsk(w, r)
	if !ok {
		return
	}
	res, _, _, err := s.check(r.Context(), chi.URLParam(r, "namespace"), ask, "")
	if err != nil {
		catalogError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func objectList(p *kube.ReleasePlan) []string {
	counts := map[string]int{}
	var kinds []string
	for _, o := range p.Objects {
		if counts[o.Kind] == 0 {
			kinds = append(kinds, o.Kind)
		}
		counts[o.Kind]++
	}
	out := make([]string, 0, len(kinds))
	for _, k := range kinds {
		if counts[k] > 1 {
			out = append(out, fmt.Sprintf("%s ×%d", k, counts[k]))
		} else {
			out = append(out, k)
		}
	}
	return out
}

func (s *Server) handleCreateRequest(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.FromContext(r.Context())
	ns := chi.URLParam(r, "namespace")
	ask, ok := s.readAsk(w, r)
	if !ok {
		return
	}
	res, ch, vals, err := s.check(r.Context(), ns, ask, "")
	if err != nil {
		catalogError(w, err)
		return
	}
	direct := s.isApprover(user.Roles)
	help := strings.TrimSpace(ask.Help)
	if len(help) > 2000 {
		help = help[:2000]
	}
	// Values that do not pass go only to an administrator, as a request for
	// help; what is installed still passes the check on approval.
	if !res.OK && (help == "" || direct) {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": "the check did not pass", "check": res})
		return
	}
	// Nothing else waiting for the same release: two requests for one name
	// would install twice, or upgrade over each other.
	if open := s.openRequestFor(r.Context(), ns, ask.Release); open != nil {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": fmt.Sprintf("%s already has a request waiting, from %s", ask.Release, open.Requester)})
		return
	}

	now := time.Now().UTC()
	req := &helmreq.Request{
		Kind: ask.Kind, Namespace: ns, Release: ask.Release,
		Repo: ask.Repo, Chart: ask.Chart, Version: ask.Version, FromVersion: res.FromVersion,
		Digest: res.Digest, Values: ask.Values, Objects: objectList(res.ReleasePlan),
		Requester: user.Username, Created: now, Expires: now.Add(s.cfg.Catalog.RequestTTL),
		Status: helmreq.StatusWaiting, NewNamespace: ask.NewNamespace, Notes: res.Notes,
		Secrets: ask.Secrets,
	}
	if !res.OK {
		req.Help = help
	}
	if direct {
		req.Status, req.Decider, req.Decided = helmreq.StatusInstalling, user.Username, &now
	}
	if err := s.catalog.requests.Create(r.Context(), req); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	op := "helm-" + ask.Kind + "-request"
	if direct {
		op = "helm-" + ask.Kind
	}
	s.audit.Log(audit.Entry{User: user.Username, Environment: s.cfg.Environment, Namespace: ns,
		Operation: op, Target: ask.Release, Allowed: true, Success: true, Detail: requestDetail(req, vals)})
	if direct {
		s.startInstall(req, ch, vals)
	}
	writeJSON(w, http.StatusCreated, req.Redacted())
}

func (s *Server) openRequestFor(ctx context.Context, ns, release string) *helmreq.Request {
	all, err := s.catalog.requests.List(ctx)
	if err != nil {
		return nil
	}
	for _, q := range all {
		if q.Namespace == ns && q.Release == release && (q.Status == helmreq.StatusWaiting || q.Status == helmreq.StatusInstalling) {
			return q
		}
	}
	return nil
}

// requestDetail is the audit line for a request: the chart, and which values
// were set — names only.
func requestDetail(q *helmreq.Request, vals map[string]any) string {
	d := q.Chart + " " + q.Version + " from " + q.Repo
	if q.FromVersion != "" {
		d = q.Chart + " " + q.FromVersion + " → " + q.Version + " from " + q.Repo
	}
	if keys := valueKeys(vals); len(keys) > 0 {
		d += "; values: " + strings.Join(keys, ", ")
	}
	if q.NewNamespace {
		d += "; new namespace"
	}
	if q.Help != "" {
		d += "; asks for help"
	}
	for _, sec := range q.Redacted().Secrets {
		d += "; creates Secret " + sec.Name + " (" + strings.Join(sec.Keys, ", ") + ")"
	}
	return d
}

// valueKeys names the leaves of a values document, a.b.c, at most a dozen.
func valueKeys(vals map[string]any) []string {
	var out []string
	var walk func(prefix string, v any)
	walk = func(prefix string, v any) {
		m, ok := v.(map[string]any)
		if !ok || len(m) == 0 {
			out = append(out, prefix)
			return
		}
		for k, sub := range m {
			p := k
			if prefix != "" {
				p = prefix + "." + k
			}
			walk(p, sub)
		}
	}
	for k, v := range vals {
		walk(k, v)
	}
	sort.Strings(out)
	if len(out) > 12 {
		out = append(out[:12], fmt.Sprintf("and %d more", len(out)-12))
	}
	return out
}

// handleMyRequests lists the requests in every namespace the caller may ask
// in or list releases in: their team's, not only their own.
func (s *Server) handleMyRequests(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.FromContext(r.Context())
	all, err := s.catalog.requests.List(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	mine := []*helmreq.Request{}
	for _, q := range all {
		if q.Requester == user.Username || s.mayAskFor(user.Roles, q.Namespace) ||
			s.authz.Allowed(user.Roles, q.Namespace, OpHelmList) {
			mine = append(mine, q)
		}
	}
	writeJSON(w, http.StatusOK, s.viewRequests(r.Context(), user, mine))
}

func (s *Server) handleWithdrawRequest(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.FromContext(r.Context())
	ns, id := chi.URLParam(r, "namespace"), chi.URLParam(r, "id")
	q, err := s.catalog.requests.Update(r.Context(), id, func(q *helmreq.Request) error {
		if q.Namespace != ns {
			return helmreq.ErrNotFound
		}
		if q.Requester != user.Username && !s.isApprover(user.Roles) {
			return errForbidden
		}
		if !q.Open() {
			return fmt.Errorf("the request is already %s", q.Status)
		}
		now := time.Now().UTC()
		q.Status, q.Decider, q.Decided = helmreq.StatusWithdrawn, user.Username, &now
		return nil
	})
	if err != nil {
		requestError(w, err)
		return
	}
	s.audit.Log(audit.Entry{User: user.Username, Environment: s.cfg.Environment, Namespace: ns,
		Operation: "helm-request-withdraw", Target: q.Release, Allowed: true, Success: true,
		Detail: q.Chart + " " + q.Version})
	writeJSON(w, http.StatusOK, q.Redacted())
}

func (s *Server) handleAllRequests(w http.ResponseWriter, r *http.Request) {
	all, err := s.catalog.requests.List(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	user, _ := auth.FromContext(r.Context())
	writeJSON(w, http.StatusOK, s.viewRequests(r.Context(), user, all))
}

func (s *Server) handleRequestCount(w http.ResponseWriter, r *http.Request) {
	all, err := s.catalog.requests.List(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	n := 0
	for _, q := range all {
		if q.Open() {
			n++
		}
	}
	writeJSON(w, http.StatusOK, map[string]int{"waiting": n})
}

// handleCheckRequest runs a request's check again for whoever answers it, with
// the values they are about to approve. Theirs to run whether or not they hold
// anything in the namespace: answering is the grant.
func (s *Server) handleCheckRequest(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Values *string `json:"values"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 512<<10)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "the request does not read: " + err.Error()})
		return
	}
	q, err := s.catalog.requests.Get(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		requestError(w, err)
		return
	}
	ask := &catalogAsk{Kind: q.Kind, Release: q.Release, Repo: q.Repo, Chart: q.Chart, Version: q.Version, Values: q.Values,
		Secrets: q.Secrets}
	if body.Values != nil {
		ask.Values = *body.Values
	}
	res, _, _, err := s.check(r.Context(), q.Namespace, ask, q.Digest)
	if err != nil {
		catalogError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleApproveRequest(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.FromContext(r.Context())
	id := chi.URLParam(r, "id")
	var body struct {
		Values *string `json:"values"`
		// Grant are the requester's roles to give the new namespace to.
		Grant []string `json:"grant"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 512<<10)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "the request does not read: " + err.Error()})
		return
	}
	q, err := s.catalog.requests.Get(r.Context(), id)
	if err != nil {
		requestError(w, err)
		return
	}
	if !q.Open() {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "the request is already " + q.Status})
		return
	}
	if len(body.Grant) > 0 {
		if !q.NewNamespace {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "access is given only with a new namespace"})
			return
		}
		held := map[string]bool{}
		for _, role := range s.store.RolesFor(q.Requester) {
			held[role] = true
		}
		for _, role := range body.Grant {
			if !held[role] {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": q.Requester + " does not hold the role " + role})
				return
			}
		}
	}
	ask := &catalogAsk{Kind: q.Kind, Release: q.Release, Repo: q.Repo, Chart: q.Chart, Version: q.Version, Values: q.Values,
		Secrets: q.Secrets}
	changed := body.Values != nil && strings.TrimSpace(*body.Values) != strings.TrimSpace(q.Values)
	if changed {
		ask.Values = *body.Values
	}
	// Checked again, against the archive the requester was shown: the cluster
	// and the chart may both have moved since.
	res, ch, vals, err := s.check(r.Context(), q.Namespace, ask, q.Digest)
	if err != nil {
		catalogError(w, err)
		return
	}
	if !res.OK {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": "the check did not pass", "check": res})
		return
	}
	q, err = s.catalog.requests.Update(r.Context(), id, func(q *helmreq.Request) error {
		if !q.Open() {
			return fmt.Errorf("the request is already %s", q.Status)
		}
		now := time.Now().UTC()
		if changed {
			asked := q.Values
			q.AskedValues, q.Values = &asked, ask.Values
		}
		q.Status, q.Decider, q.Decided = helmreq.StatusInstalling, user.Username, &now
		q.Objects = objectList(res.ReleasePlan)
		q.Notes = res.Notes
		q.GrantedRoles = body.Grant
		return nil
	})
	if err != nil {
		requestError(w, err)
		return
	}
	// Access before the install: the requester follows it from the start.
	for _, role := range body.Grant {
		if err := s.grantNamespace(r, role, q.Namespace); err != nil {
			slog.Error("give a role a new namespace", "role", role, "namespace", q.Namespace, "err", err)
		}
	}
	detail := requestDetail(q, vals) + "; requested by " + q.Requester
	if changed {
		detail += "; values changed on approval"
	}
	s.audit.Log(audit.Entry{User: user.Username, Environment: s.cfg.Environment, Namespace: q.Namespace,
		Operation: "helm-" + q.Kind, Target: q.Release, Allowed: true, Success: true, Detail: detail})
	s.startInstall(q, ch, vals)
	writeJSON(w, http.StatusOK, q.Redacted())
}

func (s *Server) handleRejectRequest(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.FromContext(r.Context())
	id := chi.URLParam(r, "id")
	var body struct {
		Comment string `json:"comment"`
	}
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&body)
	body.Comment = strings.TrimSpace(body.Comment)
	if body.Comment == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "say why, so the requester knows what to change"})
		return
	}
	q, err := s.catalog.requests.Update(r.Context(), id, func(q *helmreq.Request) error {
		if !q.Open() {
			return fmt.Errorf("the request is already %s", q.Status)
		}
		now := time.Now().UTC()
		q.Status, q.Decider, q.Decided, q.Comment = helmreq.StatusRejected, user.Username, &now, body.Comment
		return nil
	})
	if err != nil {
		requestError(w, err)
		return
	}
	s.audit.Log(audit.Entry{User: user.Username, Environment: s.cfg.Environment, Namespace: q.Namespace,
		Operation: "helm-request-reject", Target: q.Release, Allowed: true, Success: true,
		Detail: q.Chart + " " + q.Version + "; requested by " + q.Requester})
	writeJSON(w, http.StatusOK, q.Redacted())
}

// startInstall runs an approved request in the background: waiting for pods
// takes minutes, longer than anyone should hold a request open for. The
// request says how it ended.
func (s *Server) startInstall(q *helmreq.Request, ch *chart.Chart, vals map[string]any) {
	ctx, cancel := context.WithTimeout(context.Background(), s.cfg.Catalog.InstallTimeout+time.Minute)
	run := &runningInstall{namespace: q.Namespace, release: q.Release, cancel: cancel, finished: make(chan struct{})}
	s.catalog.running.add(q.ID, run)
	go func() {
		defer cancel()
		defer s.catalog.running.done(q.ID)
		err := s.catalog.installer(ctx, q, ch, vals)
		close(run.finished)
		status, detail, raw := helmreq.StatusInstalled, "", ""
		if err != nil {
			raw = err.Error()
			status, detail = helmreq.StatusFailed, failureSummary(raw, s.cfg.Catalog.InstallTimeout)
			if by, why := run.stopped(); by != "" {
				detail = "stopped by " + by
				if why != "" {
					detail += " " + why
				}
				detail += "; the release is left failed — uninstall it under Installed, or try again"
			}
			// Read afresh: the install's own time may be what ran out.
			tctx, tcancel := context.WithTimeout(context.Background(), 10*time.Second)
			if trouble := s.catalog.trouble(tctx, q.Namespace, q.Release); trouble != "" {
				detail += " — " + trouble
			}
			tcancel()
		}
		if _, uerr := s.catalog.requests.Update(context.Background(), q.ID, func(r *helmreq.Request) error {
			r.Status, r.Detail, r.Error = status, detail, raw
			return nil
		}); uerr != nil {
			slog.Error("record how a catalog install ended", "request", q.ID, "err", uerr)
		}
		s.audit.Log(audit.Entry{User: q.Decider, Environment: s.cfg.Environment, Namespace: q.Namespace,
			Operation: "helm-" + q.Kind + "-result", Target: q.Release, Allowed: true,
			Success: err == nil, Error: errStr(err), Detail: q.Chart + " " + q.Version})
	}()
}

// runInstall is the installer the cluster provides.
func (s *Server) runInstall(ctx context.Context, q *helmreq.Request, ch *chart.Chart, vals map[string]any) error {
	labels := kube.CatalogLabels(q.Repo, q.Chart)
	if q.NewNamespace && q.Kind == helmreq.KindInstall {
		if err := s.kube.CreateNamespace(ctx, q.Namespace, q.Requester); err != nil {
			return fmt.Errorf("create the namespace %s: %w", q.Namespace, err)
		}
	}
	for _, sec := range q.Secrets {
		if err := s.kube.CreateSecretFor(ctx, q.Namespace, sec.Name, sec.Data, q.Release); err != nil {
			return fmt.Errorf("create the Secret %s: %w", sec.Name, err)
		}
	}
	if q.Kind == helmreq.KindUpgrade {
		return s.kube.UpgradeRelease(ctx, q.Namespace, q.Release, ch, vals, labels, s.cfg.Catalog.InstallTimeout)
	}
	return s.kube.InstallRelease(ctx, q.Namespace, q.Release, ch, vals, labels, s.cfg.Catalog.InstallTimeout)
}

var errForbidden = errors.New("only the requester or an administrator can withdraw a request")

func requestError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, helmreq.ErrNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
	case errors.Is(err, errForbidden):
		writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
	default:
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
	}
}

// askError is something wrong with what was asked, rather than with reaching
// the catalog or the cluster.
type askError struct{ error }

func (e askError) Unwrap() error { return e.error }

func catalogError(w http.ResponseWriter, err error) {
	var ae askError
	if errors.As(err, &ae) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if errors.Is(err, helmcat.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
}
