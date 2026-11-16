// Package helmcat is the Helm catalog: what can be installed, and the charts
// themselves.
//
// The list comes from Artifact Hub, which indexes the charts people publish
// and says where each one lives. Nothing is configured per repository: a chart
// is fetched from wherever its author publishes it — an HTTP repository, an
// OCI registry — at the address Artifact Hub gives. Only charts from verified
// publishers are offered.
package helmcat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Package is one chart in the catalog, as a list shows it.
type Package struct {
	Repo        string `json:"repo"`
	RepoTitle   string `json:"repoTitle"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Version     string `json:"version"`
	AppVersion  string `json:"appVersion,omitempty"`
	Logo        string `json:"logo,omitempty"`
	Stars       int    `json:"stars"`
	Official    bool   `json:"official,omitempty"`
	Deprecated  bool   `json:"deprecated,omitempty"`
}

// Page is one page of a search.
type Page struct {
	Packages []Package `json:"packages"`
	Total    int       `json:"total"`
}

// Version is one release of a chart.
type Version struct {
	Version    string `json:"version"`
	AppVersion string `json:"appVersion,omitempty"`
	Prerelease bool   `json:"prerelease,omitempty"`
}

// Located is where one version of a chart is fetched from, with what the
// catalog says about it.
type Located struct {
	Package
	// ContentURL is the archive: https://… or oci://…
	ContentURL string `json:"-"`
	RepoURL    string `json:"repoUrl"`
	// Page is the chart's page on Artifact Hub.
	Page     string    `json:"page"`
	Readme   string    `json:"readme,omitempty"`
	Versions []Version `json:"versions"`
}

// ErrNotFound says the catalog has no such chart or version.
var ErrNotFound = errors.New("the catalog has no such chart")

// Hub reads Artifact Hub.
type Hub struct {
	base string
	http *http.Client
}

// NewHub reads the Artifact Hub at base, e.g. https://artifacthub.io.
func NewHub(base string) *Hub {
	return &Hub{base: strings.TrimRight(base, "/"), http: &http.Client{Timeout: 20 * time.Second}}
}

// PageSize is how many charts one page of a search holds.
const PageSize = 20

// Search lists charts matching q, verified publishers only.
func (h *Hub) Search(ctx context.Context, q string, offset int) (Page, error) {
	v := url.Values{}
	v.Set("kind", "0") // Helm charts
	v.Set("verified_publisher", "true")
	v.Set("limit", strconv.Itoa(PageSize))
	v.Set("offset", strconv.Itoa(max(offset, 0)))
	v.Set("facets", "false")
	if q = strings.TrimSpace(q); q != "" {
		v.Set("ts_query_web", q)
	} else {
		v.Set("sort", "stars")
	}
	var body struct {
		Packages []hubPackage `json:"packages"`
	}
	hdr, err := h.get(ctx, "/api/v1/packages/search?"+v.Encode(), &body)
	if err != nil {
		return Page{}, err
	}
	page := Page{Packages: make([]Package, 0, len(body.Packages))}
	page.Total, _ = strconv.Atoi(hdr.Get("Pagination-Total-Count"))
	for _, p := range body.Packages {
		page.Packages = append(page.Packages, p.toPackage(h.base))
	}
	return page, nil
}

// Locate finds one version of a chart; an empty version is the latest.
func (h *Hub) Locate(ctx context.Context, repo, name, version string) (*Located, error) {
	if !validName(repo) || !validName(name) || (version != "" && !validVersion(version)) {
		return nil, ErrNotFound
	}
	p := "/api/v1/packages/helm/" + url.PathEscape(repo) + "/" + url.PathEscape(name)
	if version != "" {
		p += "/" + url.PathEscape(version)
	}
	var body hubPackage
	if _, err := h.get(ctx, p, &body); err != nil {
		return nil, err
	}
	if body.ContentURL == "" {
		return nil, fmt.Errorf("the catalog does not say where %s/%s %s is published", repo, name, version)
	}
	loc := &Located{
		Package:    body.toPackage(h.base),
		ContentURL: body.ContentURL,
		RepoURL:    body.Repository.URL,
		Page:       h.base + "/packages/helm/" + url.PathEscape(repo) + "/" + url.PathEscape(name),
		Readme:     body.Readme,
		Versions:   make([]Version, 0, len(body.AvailableVersions)),
	}
	for _, v := range body.AvailableVersions {
		loc.Versions = append(loc.Versions, Version{Version: v.Version, AppVersion: v.AppVersion, Prerelease: v.Prerelease})
	}
	return loc, nil
}

// get reads one JSON document from the hub.
func (h *Hub) get(ctx context.Context, path string, into any) (http.Header, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.base+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := h.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("the catalog (%s) did not answer: %w", h.base, err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, ErrNotFound
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("the catalog (%s) answered %s", h.base, resp.Status)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(into); err != nil {
		return nil, fmt.Errorf("the catalog's answer does not read: %w", err)
	}
	return resp.Header, nil
}

// hubPackage is a package as Artifact Hub writes it.
type hubPackage struct {
	Name              string `json:"name"`
	Description       string `json:"description"`
	Version           string `json:"version"`
	AppVersion        string `json:"app_version"`
	LogoImageID       string `json:"logo_image_id"`
	Stars             int    `json:"stars"`
	Official          bool   `json:"official"`
	Deprecated        bool   `json:"deprecated"`
	ContentURL        string `json:"content_url"`
	Readme            string `json:"readme"`
	AvailableVersions []struct {
		Version    string `json:"version"`
		AppVersion string `json:"app_version"`
		Prerelease bool   `json:"prerelease"`
	} `json:"available_versions"`
	Repository struct {
		Name        string `json:"name"`
		DisplayName string `json:"display_name"`
		URL         string `json:"url"`
		Official    bool   `json:"official"`
	} `json:"repository"`
}

func (p hubPackage) toPackage(base string) Package {
	out := Package{
		Repo: p.Repository.Name, RepoTitle: p.Repository.DisplayName,
		Name: p.Name, Description: p.Description, Version: p.Version, AppVersion: p.AppVersion,
		Stars: p.Stars, Official: p.Official || p.Repository.Official, Deprecated: p.Deprecated,
	}
	if out.RepoTitle == "" {
		out.RepoTitle = out.Repo
	}
	if p.LogoImageID != "" && validName(p.LogoImageID) {
		out.Logo = base + "/image/" + p.LogoImageID
	}
	return out
}

var (
	nameRE    = regexp.MustCompile(`^[a-zA-Z0-9][-a-zA-Z0-9_.]{0,127}$`)
	versionRE = regexp.MustCompile(`^v?[0-9][-+a-zA-Z0-9_.]{0,63}$`)
)

func validName(s string) bool    { return nameRE.MatchString(s) }
func validVersion(s string) bool { return versionRE.MatchString(s) }
