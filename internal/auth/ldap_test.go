package auth

import (
	"testing"

	"github.com/go-ldap/ldap/v3"

	"devops-tools/internal/config"
)

// StartTLS fails outright without a server name to verify against:
//
//	tls: either ServerName or InsecureSkipVerify must be specified
//
// It went unnoticed because ldaps:// works — there the library fills the name
// in from the URL and never consults ours. StartTLS hands this config to Go
// unchanged, so the omission only shows up on the first sign-in of a
// deployment configured that way.
func TestServerNameIsTakenFromTheURL(t *testing.T) {
	cases := map[string]string{
		"ldap://example.com:389":     "example.com",
		"ldaps://dc.example.com":     "dc.example.com",
		"ldaps://dc.example.com:636": "dc.example.com",
		// An address rather than a name: passed through as it is, and the
		// certificate has to carry it. Nothing here can guess otherwise.
		"ldap://10.13.1.36:389": "10.13.1.36",
	}
	for rawURL, want := range cases {
		p, err := newLDAPProvider(config.LDAPConfig{
			URL: rawURL,
			TLS: config.LDAPTLSConfig{StartTLS: true},
		})
		if err != nil {
			t.Fatalf("newLDAPProvider(%q): %v", rawURL, err)
		}
		if p.tls.ServerName != want {
			t.Errorf("%q → ServerName %q, want %q", rawURL, p.tls.ServerName, want)
		}
	}
}

// Verification stays on unless it is switched off deliberately: a TLS config
// that verifies nothing is barely different from no TLS at all.
func TestVerificationIsOnByDefault(t *testing.T) {
	p, err := newLDAPProvider(config.LDAPConfig{
		URL: "ldaps://dc.example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	if p.tls.InsecureSkipVerify {
		t.Error("certificate verification is off without anybody asking for it")
	}
	if p.tls.MinVersion < 0x0303 { // TLS 1.2
		t.Errorf("MinVersion %#x, want TLS 1.2 or later", p.tls.MinVersion)
	}
}

// Each group used to be recorded twice, as its DN and again as its bare name,
// so that groupMapping could be written either way. groupMapping is now refused
// at startup, and the leftover copy turned a real account — twenty-odd groups —
// into forty entries filling the Users table.
func TestEachGroupIsRecordedOnce(t *testing.T) {
	p, err := newLDAPProvider(config.LDAPConfig{URL: "ldaps://dc.example.com"})
	if err != nil {
		t.Fatal(err)
	}

	dns := []string{
		"CN=payments-read,OU=Groups,DC=example,DC=com",
		"CN=developers,OU=Groups,DC=example,DC=com",
	}
	entry := &ldap.Entry{
		DN: "CN=someone,OU=People,DC=example,DC=com",
		Attributes: []*ldap.EntryAttribute{
			{Name: "memberOf", Values: dns},
		},
	}

	// No group search configured, so memberOf is the source and no connection is
	// touched — which is what makes this reachable without a directory.
	got, err := p.groupsOf(nil, entry, "someone")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(dns) {
		t.Fatalf("groupsOf returned %d entries for %d groups: %q", len(got), len(dns), got)
	}
	// The DN, not the shortened name: the page cuts one down for display, and
	// cutting cannot be undone if the name is all that was kept.
	for i, want := range dns {
		if got[i] != want {
			t.Errorf("group %d = %q, want %q", i, got[i], want)
		}
	}
}
