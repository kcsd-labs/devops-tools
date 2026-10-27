package configsource

import "testing"

// historied is a source that keeps history, the way Git does.
type historied struct{ *side }

func (h historied) RevisionURL(version string) string {
	return "https://gitlab.example.com/team/configs/-/commit/" + version
}

// The link to a commit has to survive both wrappers a deployment puts around
// the repository. An embedded interface does not carry the optional methods of
// what it wraps, so without the pass-throughs the audit record would quietly
// lose its link in exactly the configurations that have one.
func TestTheCommitLinkSurvivesTheWrappers(t *testing.T) {
	git := historied{newSide(nil)}
	want := "https://gitlab.example.com/team/configs/-/commit/abc123"

	cases := map[string]Source{
		"git":             git,
		"cached git":      Cache(git),
		"git+consul":      NewPair(Cache(git), newSide(nil)),
		"git+consul bare": NewPair(git, newSide(nil)),
	}
	for name, src := range cases {
		if got := RevisionURL(src, "abc123"); got != want {
			t.Errorf("%s: RevisionURL = %q, want %q", name, got, want)
		}
	}
}

// Consul keeps no history; there is nothing to link to, and saying so beats
// inventing a link.
func TestASourceWithoutHistoryHasNoLink(t *testing.T) {
	for name, src := range map[string]Source{
		"consul":        newSide(nil),
		"cached consul": Cache(newSide(nil)),
	} {
		if got := RevisionURL(src, "17"); got != "" {
			t.Errorf("%s: RevisionURL = %q, want none", name, got)
		}
	}
	if got := RevisionURL(historied{newSide(nil)}, ""); got != "" {
		t.Errorf("an empty version produced a link: %q", got)
	}
}
