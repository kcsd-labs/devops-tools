package gitlab

import (
	"fmt"
	"strconv"
	"strings"
)

// Working out which pipeline built a running container.
//
// This exists because an image can disappear from the registry — a retention
// policy removes old tags — while the workload that runs it is still deployed.
// The pipeline that produced the tag can simply be run again, and the image
// comes back under the same name. Finding that pipeline means reading it out of
// the image reference, since nothing else records the link.
//
// The tag convention is a local one, so it is configurable rather than assumed.

// TagConvention describes how a deployment's image tags are built.
//
// Naming is a house style: the code cannot guess it, and guessing wrong means
// retrying a stranger's pipeline. It is stated in the deployment's values.
type TagConvention struct {
	// Environments are the tag prefixes that name an environment, e.g.
	// {"dev", "preprod"}. A tag beginning with anything else is not one of ours
	// and is refused rather than interpreted.
	Environments []string
	// BuildJobs are the job names to look for, with {env} substituted. The
	// first one present in the pipeline is the one retried — deployments
	// migrating between builders have both names in circulation.
	BuildJobs []string
	// DeployJobs are the manual jobs that may be released. Building an image
	// and putting it into an environment are different acts, so they are named
	// separately here and permitted separately in the access model.
	DeployJobs []string
}

// ImageRef is what could be read out of an image reference.
type ImageRef struct {
	// ProjectPath is the GitLab project: the image path with the registry host
	// removed. The two are kept in step by convention, which is what makes this
	// possible at all.
	ProjectPath string
	Environment string
	Pipeline    int
}

// BuildJobNames returns the job names to try, in order.
func (r ImageRef) BuildJobNames(c TagConvention) []string {
	return substitute(c.BuildJobs, r.Environment)
}

// DeployJobNames returns the manual jobs that may be released for this
// environment.
func (r ImageRef) DeployJobNames(c TagConvention) []string {
	return substitute(c.DeployJobs, r.Environment)
}

func substitute(templates []string, env string) []string {
	out := make([]string, 0, len(templates))
	for _, tpl := range templates {
		out = append(out, strings.ReplaceAll(tpl, "{env}", env))
	}
	return out
}

// ParseImage reads an image reference of the form
//
//	<registry-host>[:port]/<project/path>/<name>:<env>[-anything]-<pipeline>
//
// The registry host is not hard-coded. Registries get renamed and replaced, and
// an installation usually has several in circulation at once; a list to keep up
// to date would be a list that is out of date. Docker's own rule is used
// instead: the first segment is a registry if it contains a dot or a colon.
func ParseImage(image string, conv TagConvention) (ImageRef, error) {
	var ref ImageRef

	// The tag is separated by a colon after the last slash. Before it, a colon
	// is a registry port.
	lastSlash := strings.LastIndex(image, "/")
	if lastSlash < 0 {
		return ref, fmt.Errorf("%q does not look like an image reference", image)
	}
	colon := strings.LastIndex(image[lastSlash:], ":")
	if colon < 0 {
		return ref, fmt.Errorf("image %q has no tag, so there is no pipeline to find", image)
	}
	repo := image[:lastSlash+colon]
	tag := image[lastSlash+colon+1:]

	i := strings.IndexByte(repo, '/')
	if i <= 0 {
		return ref, fmt.Errorf("could not tell the project path from %q", image)
	}
	if first := repo[:i]; strings.ContainsAny(first, ".:") {
		ref.ProjectPath = repo[i+1:]
	} else {
		ref.ProjectPath = repo
	}
	if ref.ProjectPath == "" {
		return ref, fmt.Errorf("the project path in %q is empty", image)
	}

	parts := strings.Split(tag, "-")
	if len(parts) < 2 {
		return ref, fmt.Errorf("tag %q is not of the form <environment>-…-<pipeline>", tag)
	}
	ref.Environment = parts[0]
	if !contains(conv.Environments, ref.Environment) {
		// Refused rather than interpreted. A tag written by something else is
		// not evidence about any pipeline, and acting on it would retry a build
		// belonging to somebody unrelated.
		return ref, fmt.Errorf("tag %q begins with %q, which is not one of the configured environments (%s)",
			tag, ref.Environment, strings.Join(conv.Environments, ", "))
	}
	// The last numeric segment. A tag may carry anything in between — a debug
	// marker, a short commit — and taking the last one steps over all of it.
	pipeline, err := strconv.Atoi(parts[len(parts)-1])
	if err != nil {
		return ref, fmt.Errorf("no pipeline number at the end of tag %q", tag)
	}
	ref.Pipeline = pipeline
	return ref, nil
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
