package config

import "testing"

func TestMatchesNamespace(t *testing.T) {
	patterns := []string{"kube-*", "default"}
	tests := []struct {
		name   string
		hidden bool
	}{
		{"kube-system", true},
		{"kube-public", true},
		{"kube-node-lease", true},
		{"default", true},
		// Prefix matching must not reach past the pattern.
		{"kubernetes-dashboard", false},
		{"payments-dev", false},
		// "default" is exact, so a namespace merely starting with it stays.
		{"default-backend", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := MatchesNamespace(tc.name, patterns); got != tc.hidden {
				t.Fatalf("MatchesNamespace(%q) = %v, want %v", tc.name, got, tc.hidden)
			}
		})
	}
}

// An empty configuration hides nothing — that is how an operator asks to see
// every namespace, including the cluster's own.
func TestMatchesNamespaceEmpty(t *testing.T) {
	if MatchesNamespace("kube-system", nil) {
		t.Fatal("no patterns should hide nothing")
	}
	if MatchesNamespace("kube-system", []string{}) {
		t.Fatal("an empty pattern list should hide nothing")
	}
}
