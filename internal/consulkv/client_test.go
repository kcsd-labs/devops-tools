package consulkv

import "testing"

// The prefix is joined to a path by plain concatenation, so it has to end in
// exactly one slash however it was written in the values file. "config" and
// "config/" must address the same keys — the alternative is a deployment that
// silently reads and writes "configabs/application.yml".
func TestPrefixIsNormalisedHoweverItIsWritten(t *testing.T) {
	for _, in := range []string{"config", "config/", "/config", "/config/"} {
		if got := normalisePrefix(in); got != "config/" {
			t.Errorf("normalisePrefix(%q) = %q, want %q", in, got, "config/")
		}
	}
	if got := normalisePrefix(""); got != "" {
		t.Errorf("normalisePrefix(\"\") = %q, want the empty prefix left alone", got)
	}
}
