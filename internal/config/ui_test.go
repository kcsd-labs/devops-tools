package config

import "testing"

func TestBrandInitials(t *testing.T) {
	tests := []struct {
		name, initials, want string
	}{
		{"DevOps Tools", "", "DT"},
		{"DevOps Tools", "dt", "DT"},    // explicit, and upper-cased
		{"Platform", "", "PL"},          // one word: its first two letters
		{"Ünïcode Portal", "", "ÜP"},    // runes, not bytes
		{"", "", "DT"},                  // nothing to derive from
	}
	for _, tc := range tests {
		got := UIConfig{BrandName: tc.name, BrandInitials: tc.initials}.Initials()
		if got != tc.want {
			t.Errorf("UIConfig{%q, %q}.Initials() = %q, want %q", tc.name, tc.initials, got, tc.want)
		}
	}
}

// An installation that configures nothing must look exactly as it did before
// the name became a setting.
func TestDefaultBrandIsUnchanged(t *testing.T) {
	c := &Config{}
	applyDefaults(c)
	if c.UI.BrandName != "DevOps Tools" {
		t.Fatalf("default name = %q", c.UI.BrandName)
	}
	if got := c.UI.Initials(); got != "DT" {
		t.Fatalf("default mark = %q", got)
	}
}

// Naming the product must not silently change the mark someone chose.
func TestExplicitInitialsSurviveDefaults(t *testing.T) {
	c := &Config{}
	c.UI.BrandInitials = "XY"
	applyDefaults(c)
	if got := c.UI.Initials(); got != "XY" {
		t.Fatalf("mark = %q", got)
	}
}
