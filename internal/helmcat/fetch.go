package helmcat

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/registry"
)

// maxArchive is the largest chart archive fetched. Charts are tens of
// kilobytes; one of megabytes is either vendoring something it should not or
// is not a chart.
const maxArchive = 20 << 20

// Archive is a fetched chart.
type Archive struct {
	Chart *chart.Chart
	// Digest is the archive's sha256, written sha256:<hex>. What an
	// administrator approves is this exact archive: if the author replaces
	// the version afterwards, the install is refused rather than given
	// something nobody looked at.
	Digest string
}

// Fetcher downloads chart archives and keeps the last few, since the same
// version is read for its values, checked, and installed in short order.
//
// What is kept is the archive, not the loaded chart: Helm changes a chart as
// it renders it — dependencies switched off by the values are taken out — so
// each use loads its own from the kept bytes.
type Fetcher struct {
	http *http.Client

	mu    sync.Mutex
	cache map[string][]byte
	order []string
}

// NewFetcher returns a fetcher.
func NewFetcher() *Fetcher {
	return &Fetcher{http: &http.Client{Timeout: 60 * time.Second}, cache: map[string][]byte{}}
}

const cacheSize = 24

// Fetch downloads the archive at contentURL — https://…/x.tgz or
// oci://registry/repo/chart:version — and loads the chart in it.
func (f *Fetcher) Fetch(ctx context.Context, contentURL string) (*Archive, error) {
	f.mu.Lock()
	data, ok := f.cache[contentURL]
	f.mu.Unlock()
	if !ok {
		var err error
		if data, err = f.download(ctx, contentURL); err != nil {
			return nil, err
		}
	}
	ch, err := loader.LoadArchive(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("%s is not a chart that loads: %w", contentURL, err)
	}
	sum := sha256.Sum256(data)
	a := &Archive{Chart: ch, Digest: "sha256:" + hex.EncodeToString(sum[:])}
	if ok {
		return a, nil
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.cache[contentURL]; !ok {
		f.cache[contentURL] = data
		f.order = append(f.order, contentURL)
		if len(f.order) > cacheSize {
			delete(f.cache, f.order[0])
			f.order = f.order[1:]
		}
	}
	return a, nil
}

func (f *Fetcher) download(ctx context.Context, contentURL string) ([]byte, error) {
	if ref, ok := strings.CutPrefix(contentURL, "oci://"); ok {
		return f.pullOCI(ref)
	}
	u, err := url.Parse(contentURL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, fmt.Errorf("%q is not an address a chart is fetched from", contentURL)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, contentURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := f.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("could not fetch the chart from %s: %w", u.Host, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s answered %s for the chart", u.Host, resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxArchive+1))
	if err != nil {
		return nil, fmt.Errorf("reading the chart from %s: %w", u.Host, err)
	}
	if len(data) > maxArchive {
		return nil, fmt.Errorf("the chart from %s is larger than %d MiB", u.Host, maxArchive>>20)
	}
	return data, nil
}

func (f *Fetcher) pullOCI(ref string) ([]byte, error) {
	client, err := registry.NewClient(registry.ClientOptHTTPClient(f.http), registry.ClientOptEnableCache(true))
	if err != nil {
		return nil, fmt.Errorf("prepare the registry client: %w", err)
	}
	res, err := client.Pull(ref, registry.PullOptWithChart(true))
	if err != nil {
		return nil, fmt.Errorf("could not pull the chart %s: %w", ref, err)
	}
	if res.Chart == nil || len(res.Chart.Data) == 0 {
		return nil, fmt.Errorf("%s holds no chart", ref)
	}
	if len(res.Chart.Data) > maxArchive {
		return nil, fmt.Errorf("the chart %s is larger than %d MiB", ref, maxArchive>>20)
	}
	return res.Chart.Data, nil
}
