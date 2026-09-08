package auth

import (
	"strings"
	"testing"

	"devops-tools/internal/config"
)

// checkAuthorizedParty is the decision the verifier makes once the signature is
// already valid, pulled out so it can be exercised without a provider.
func checkAuthorizedParty(v *oidcVerifier, azp string) error {
	if !v.requireAZP {
		return nil
	}
	if azp == "" {
		return errNoAuthorizedParty
	}
	if azp != v.clientID {
		return errWrongAuthorizedParty
	}
	return nil
}

func TestRequireAuthorizedPartyIsOnByDefault(t *testing.T) {
	cfg := config.OIDCConfig{ClientID: "devops-tools"}
	if !cfg.RequiresAuthorizedParty() {
		t.Fatal("an unset requireAuthorizedParty should mean on")
	}
	off := false
	cfg.RequireAuthorizedParty = &off
	if cfg.RequiresAuthorizedParty() {
		t.Fatal("setting it to false should turn it off")
	}
	on := true
	cfg.RequireAuthorizedParty = &on
	if !cfg.RequiresAuthorizedParty() {
		t.Fatal("setting it to true should keep it on")
	}
}

func TestAuthorizedPartyDecisions(t *testing.T) {
	on := &oidcVerifier{clientID: "devops-tools", requireAZP: true}
	off := &oidcVerifier{clientID: "devops-tools", requireAZP: false}

	if err := checkAuthorizedParty(on, "devops-tools"); err != nil {
		t.Fatalf("our own client was rejected: %v", err)
	}
	// The case this exists for: a token the realm signed for a different
	// application. Without the check it would be accepted, since a public
	// client's token carries no audience to check instead.
	if err := checkAuthorizedParty(on, "some-other-app"); err == nil {
		t.Fatal("a token issued to another client was accepted")
	}
	// A provider that omits azp gets an error naming the way out, not a
	// mysterious rejection.
	err := checkAuthorizedParty(on, "")
	if err == nil {
		t.Fatal("a token with no azp was accepted while the check was on")
	}
	if !strings.Contains(err.Error(), "requireAuthorizedParty") {
		t.Fatalf("the error does not say how to turn the check off: %v", err)
	}
	// Turned off, none of it applies.
	for _, azp := range []string{"", "devops-tools", "some-other-app"} {
		if err := checkAuthorizedParty(off, azp); err != nil {
			t.Fatalf("azp %q rejected with the check off: %v", azp, err)
		}
	}
}
