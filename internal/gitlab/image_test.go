package gitlab

import "testing"

var conv = TagConvention{
	Environments: []string{"dev", "preprod"},
	BuildJobs:    []string{"docker-image-{env}", "kaniko-image-{env}"},
}

// Whatever this returns is used to retry somebody's pipeline. Reading it wrong
// does not fail — it succeeds against the wrong project or the wrong build.
func TestParseImage(t *testing.T) {
	cases := []struct {
		name         string
		image        string
		wantProject  string
		wantEnv      string
		wantPipeline int
	}{
		{
			"registry with a port",
			"registry.example.com:5000/group/project/api-docs:dev-4321",
			"group/project/api-docs", "dev", 4321,
		},
		{
			"registry without a port",
			"registry.example.com/group/project/infrastructure/api-docs:preprod-99",
			"group/project/infrastructure/api-docs", "preprod", 99,
		},
		{
			// The middle of a tag carries whatever the pipeline put there. The
			// pipeline number is the last numeric segment, not the second.
			"debug marker between environment and pipeline",
			"registry.example.com/group/project/api-docs:dev-debug-4321",
			"group/project/api-docs", "dev", 4321,
		},
		{
			"several markers",
			"registry.example.com/g/p/svc:dev-debug-a1b2c3-777",
			"g/p/svc", "dev", 777,
		},
		{
			// No dot and no colon in the first segment, so by Docker's own rule
			// it is not a registry — it is part of the project path.
			"no registry host at all",
			"group/project/api-docs:dev-12",
			"group/project/api-docs", "dev", 12,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ref, err := ParseImage(c.image, conv)
			if err != nil {
				t.Fatalf("ParseImage: %v", err)
			}
			if ref.ProjectPath != c.wantProject {
				t.Errorf("ProjectPath = %q, want %q", ref.ProjectPath, c.wantProject)
			}
			if ref.Environment != c.wantEnv {
				t.Errorf("Environment = %q, want %q", ref.Environment, c.wantEnv)
			}
			if ref.Pipeline != c.wantPipeline {
				t.Errorf("Pipeline = %d, want %d", ref.Pipeline, c.wantPipeline)
			}
		})
	}
}

// A tag written by something else is not evidence about any pipeline of ours.
// Interpreting it anyway would retry a build belonging to a stranger — the one
// outcome worse than doing nothing.
func TestForeignTagsAreRefusedRatherThanInterpreted(t *testing.T) {
	refused := []string{
		"docker.io/library/postgres:16.2",    // no environment prefix
		"registry.example.com/g/p/svc:latest",       // no pipeline, no environment
		"registry.example.com/g/p/svc:staging-4321", // an environment we do not know
		"registry.example.com/g/p/svc:dev-nightly",  // nothing numeric at the end
		"registry.example.com/g/p/svc",              // no tag at all
		"postgres:16",                        // no path to be a project
	}
	for _, image := range refused {
		if ref, err := ParseImage(image, conv); err == nil {
			t.Errorf("ParseImage(%q) = %+v, want an error", image, ref)
		}
	}
}

// A digest is not a tag: there is no pipeline number in it, and the pipeline
// that built it cannot be recovered this way.
func TestDigestPinnedImageIsRefused(t *testing.T) {
	const image = "registry.example.com/g/p/svc@sha256:" +
		"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	if ref, err := ParseImage(image, conv); err == nil {
		t.Errorf("ParseImage on a digest = %+v, want an error", ref)
	}
}

// Both builders are in circulation while a deployment migrates between them, so
// both names are offered and the one actually present is used.
func TestBuildJobNamesSubstituteTheEnvironment(t *testing.T) {
	ref := ImageRef{Environment: "preprod"}
	got := ref.BuildJobNames(conv)
	want := []string{"docker-image-preprod", "kaniko-image-preprod"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("job[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
