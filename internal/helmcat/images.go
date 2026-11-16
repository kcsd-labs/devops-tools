package helmcat

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"oras.land/oras-go/v2/errdef"
	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"
)

// ImageState is what a registry said about one image.
type ImageState struct {
	Image string `json:"image"`
	// Missing: the registry answered and has no such image — a chart that
	// installs and then never starts. Bitnami's charts after its images moved
	// are the case everyone meets.
	Missing bool `json:"missing,omitempty"`
	// Unknown: the registry could not be asked, or wants a login. Not a
	// verdict either way.
	Unknown string `json:"unknown,omitempty"`
}

// CheckImages asks each image's registry whether it has the image, without
// pulling it. Anonymous HEAD requests: they do not count against Docker Hub's
// pull limit.
func CheckImages(ctx context.Context, images []string) []ImageState {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	client := &auth.Client{Client: &http.Client{Timeout: 10 * time.Second}, Cache: auth.NewCache()}

	uniq := map[string]bool{}
	for _, im := range images {
		if im = strings.TrimSpace(im); im != "" {
			uniq[im] = true
		}
	}
	out := make([]ImageState, 0, len(uniq))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for im := range uniq {
		wg.Add(1)
		go func(im string) {
			defer wg.Done()
			st := checkImage(ctx, client, im)
			mu.Lock()
			out = append(out, st)
			mu.Unlock()
		}(im)
	}
	wg.Wait()
	sort.Slice(out, func(i, j int) bool { return out[i].Image < out[j].Image })
	return out
}

func checkImage(ctx context.Context, client *auth.Client, image string) ImageState {
	st := ImageState{Image: image}
	repo, ref, err := splitImage(image)
	if err != nil {
		st.Unknown = err.Error()
		return st
	}
	r, err := remote.NewRepository(repo)
	if err != nil {
		st.Unknown = "not an image reference"
		return st
	}
	r.Client = client
	if _, err := r.Resolve(ctx, ref); err != nil {
		if errors.Is(err, errdef.ErrNotFound) || strings.Contains(err.Error(), "MANIFEST_UNKNOWN") ||
			strings.Contains(err.Error(), "NAME_UNKNOWN") {
			st.Missing = true
			return st
		}
		st.Unknown = shortRegistryError(err)
	}
	return st
}

// splitImage turns an image as a pod spec writes it into the repository and
// the tag or digest a registry is asked for: "redis:7" is
// registry-1.docker.io/library/redis and "7".
func splitImage(image string) (repo, ref string, err error) {
	name := image
	ref = "latest"
	if at := strings.Index(name, "@"); at >= 0 {
		name, ref = name[:at], name[at+1:]
	} else if colon := strings.LastIndex(name, ":"); colon > strings.LastIndex(name, "/") {
		name, ref = name[:colon], name[colon+1:]
	}
	if name == "" || ref == "" {
		return "", "", fmt.Errorf("%q is not an image reference", image)
	}
	host, path := "docker.io", name
	if slash := strings.Index(name, "/"); slash >= 0 {
		first := name[:slash]
		if strings.ContainsAny(first, ".:") || first == "localhost" {
			host, path = first, name[slash+1:]
		}
	}
	if host == "docker.io" || host == "index.docker.io" {
		host = "registry-1.docker.io"
		if !strings.Contains(path, "/") {
			path = "library/" + path
		}
	}
	return host + "/" + path, ref, nil
}

func shortRegistryError(err error) string {
	s := err.Error()
	switch {
	case strings.Contains(s, "401") || strings.Contains(s, "UNAUTHORIZED") || strings.Contains(s, "403"):
		return "the registry wants a login to say"
	case errors.Is(err, context.DeadlineExceeded) || strings.Contains(s, "timeout") || strings.Contains(s, "deadline"):
		return "the registry did not answer"
	}
	if len(s) > 160 {
		s = s[:160] + "…"
	}
	return s
}
