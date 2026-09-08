package auth

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strings"

	"github.com/go-ldap/ldap/v3"

	"devops-tools/internal/config"
)

// ldapProvider authenticates against Active Directory or OpenLDAP.
//
// The login sequence is the standard search-then-bind:
//  1. connect and bind as the service account (or anonymously),
//  2. search for the user entry with the configured filter,
//  3. re-bind as the user's DN with the supplied password — this is what
//     actually verifies the password,
//  4. collect the user's groups and map them to application roles.
//
// A fresh connection is used per login: logins are infrequent and a pool would
// add failure modes (stale sockets, rebind races) for no real benefit.
type ldapProvider struct {
	cfg config.LDAPConfig
	tls *tls.Config
}

func newLDAPProvider(cfg config.LDAPConfig) (*ldapProvider, error) {
	tlsCfg := &tls.Config{
		InsecureSkipVerify: cfg.TLS.InsecureSkipVerify, //nolint:gosec // opt-in, documented as unsafe
		MinVersion:         tls.VersionTLS12,
		// Whose certificate we expect. With ldaps:// the library fills this in
		// from the URL, which is why it can be forgotten; StartTLS hands this
		// config straight to Go, and Go refuses to verify a certificate without
		// knowing which name to check it against:
		//
		//   tls: either ServerName or InsecureSkipVerify must be specified
		//
		// Same value either way — the host being connected to.
		ServerName: ldapServerName(cfg.URL),
	}
	if cfg.TLS.InsecureSkipVerify {
		slog.Warn("ldap: certificate verification is disabled (auth.ldap.tls.insecureSkipVerify)")
	}
	if cfg.TLS.CAFile != "" {
		pem, err := os.ReadFile(cfg.TLS.CAFile)
		if err != nil {
			return nil, fmt.Errorf("ldap: read caFile: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("ldap: caFile %q contains no certificates", cfg.TLS.CAFile)
		}
		tlsCfg.RootCAs = pool
	}
	return &ldapProvider{cfg: cfg, tls: tlsCfg}, nil
}

// ldapServerName is the host from the directory URL, which is the name its
// certificate has to carry. An unparseable URL is left to fail later, at the
// connection, where the error says so plainly.
func ldapServerName(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

func (p *ldapProvider) connect() (*ldap.Conn, error) {
	conn, err := ldap.DialURL(p.cfg.URL, ldap.DialWithTLSConfig(p.tls))
	if err != nil {
		return nil, fmt.Errorf("ldap: connect %s: %w", p.cfg.URL, err)
	}
	if p.cfg.TLS.StartTLS {
		if err := conn.StartTLS(p.tls); err != nil {
			conn.Close()
			return nil, fmt.Errorf("ldap: starttls: %w", err)
		}
	}
	return conn, nil
}

func (p *ldapProvider) Login(_ context.Context, username, password string) (*User, error) {
	// An empty password would be accepted by some directories as an anonymous
	// bind, which must never count as a successful login.
	if password == "" || username == "" {
		return nil, errInvalidCredentials
	}

	conn, err := p.connect()
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	// 1. bind as the service account so we may search
	if p.cfg.BindDN != "" {
		if err := conn.Bind(p.cfg.BindDN, p.cfg.BindPassword); err != nil {
			return nil, fmt.Errorf("ldap: service account bind failed: %w", err)
		}
	}

	// 2. find the user entry (escape the input — it goes into a filter)
	safeUser := ldap.EscapeFilter(username)
	filter := strings.ReplaceAll(p.cfg.UserFilter, "%s", safeUser)
	attrs := []string{"dn", p.cfg.EmailAttribute, "memberOf"}
	res, err := conn.Search(ldap.NewSearchRequest(
		p.cfg.UserSearchBase, ldap.ScopeWholeSubtree, ldap.NeverDerefAliases,
		2, 10, false, filter, attrs, nil,
	))
	if err != nil {
		return nil, fmt.Errorf("ldap: user search failed: %w", err)
	}
	if len(res.Entries) != 1 {
		// zero matches, or an ambiguous filter — both are a failed login
		return nil, errInvalidCredentials
	}
	entry := res.Entries[0]

	// 3. bind as the user — this verifies the password
	if err := conn.Bind(entry.DN, password); err != nil {
		return nil, errInvalidCredentials
	}

	// 4. collect groups and map them to roles
	groups, err := p.groupsOf(conn, entry, safeUser)
	if err != nil {
		// Groups drive authorization, so failing to read them means we cannot
		// tell what the user may do. Refuse rather than grant a bare session.
		return nil, fmt.Errorf("ldap: could not read groups for %q: %w", username, err)
	}

	// No roles: the directory says who this is, the portal says what they may
	// do. Someone signing in for the first time appears under Users with no
	// access until an administrator grants some.
	return &User{
		Username: username,
		Email:    entry.GetAttributeValue(p.cfg.EmailAttribute),
		Groups:   groups,
	}, nil
}

// groupsOf returns one entry per group.
//
// It used to return each group twice — the DN and the bare name — because
// groupMapping could be written either way. That setting is now refused at
// startup and groups grant nothing, so the second copy bought nothing and cost
// a table cell: somebody in twenty directory groups produced forty entries.
//
// What is returned is the fullest form available, since the display name can
// be cut from a DN but not reconstructed from one.
func (p *ldapProvider) groupsOf(conn *ldap.Conn, entry *ldap.Entry, safeUser string) ([]string, error) {
	// No group search configured: trust the memberOf attribute on the user.
	if p.cfg.GroupSearchBase == "" || p.cfg.GroupFilter == "" {
		return entry.GetAttributeValues("memberOf"), nil
	}

	filter := strings.ReplaceAll(p.cfg.GroupFilter, "%s", ldap.EscapeFilter(entry.DN))
	filter = strings.ReplaceAll(filter, "%u", safeUser)
	res, err := conn.Search(ldap.NewSearchRequest(
		p.cfg.GroupSearchBase, ldap.ScopeWholeSubtree, ldap.NeverDerefAliases,
		0, 10, false, filter, []string{"dn", p.cfg.GroupNameAttribute}, nil,
	))
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(res.Entries))
	for _, e := range res.Entries {
		// groupNameAttribute was configured to say what these are called; the DN
		// is the fallback for a directory that does not carry that attribute.
		if name := e.GetAttributeValue(p.cfg.GroupNameAttribute); name != "" {
			out = append(out, name)
			continue
		}
		out = append(out, e.DN)
	}
	return out, nil
}

func (p *ldapProvider) Close() {}
