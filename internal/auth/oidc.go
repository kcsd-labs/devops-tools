package auth

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/coreos/go-oidc/v3/oidc"

	"devops-tools/internal/config"
)

// oidcVerifier validates tokens issued by an external identity provider.
//
// It establishes who someone is and nothing more. Roles are held in the portal
// and assigned there, so the realm and client roles the provider carries are
// not read at all — a role granted in Keycloak and a role granted here would
// otherwise be two sources of truth for the same question, and revoking access
// in one would not revoke it in the other.
//
// A user is identified by preferred_username. Renaming them in the provider
// therefore produces a new record, without the roles the old one held; the
// alternative, keying on the immutable sub, would put a UUID in the audit log
// and in the bootstrap list.
type oidcVerifier struct {
	verifier   *oidc.IDTokenVerifier
	clientID   string
	requireAZP bool
	// described reports the shape of a verified token once per process. Which
	// audience a provider puts in its tokens decides whether the audience check
	// can be left on, and that is otherwise only discoverable by decoding one
	// by hand.
	described sync.Once
}

func newOIDCVerifier(ctx context.Context, cfg config.OIDCConfig) (*oidcVerifier, error) {
	provider, err := oidc.NewProvider(ctx, cfg.IssuerURL)
	if err != nil {
		return nil, fmt.Errorf("oidc discovery for %s: %w", cfg.IssuerURL, err)
	}
	v := provider.Verifier(&oidc.Config{
		ClientID:          cfg.ClientID,
		SkipClientIDCheck: cfg.SkipAudienceCheck,
	})
	return &oidcVerifier{
		verifier:   v,
		clientID:   cfg.ClientID,
		requireAZP: cfg.RequiresAuthorizedParty(),
	}, nil
}

// standardClaims covers the fields we read from an access token.
type standardClaims struct {
	Subject           string `json:"sub"`
	PreferredUsername string `json:"preferred_username"`
	Name              string `json:"name"`
	Email             string `json:"email"`
	// Groups do not grant anything. They are recorded and shown next to each
	// user as the context for deciding what to grant. Keycloak does not emit
	// them until a group membership mapper is added to the client scope.
	Groups []string `json:"groups"`
	// AuthorizedParty is the client the token was issued to. Unlike the
	// audience it always names this client, which is what makes it the thing
	// to check when the audience cannot be.
	AuthorizedParty string `json:"azp"`
}

func (v *oidcVerifier) Verify(ctx context.Context, rawToken string) (*User, error) {
	token, err := v.verifier.Verify(ctx, rawToken)
	if err != nil {
		return nil, fmt.Errorf("verify token: %w", err)
	}

	var c standardClaims
	if err := token.Claims(&c); err != nil {
		return nil, fmt.Errorf("parse claims: %w", err)
	}

	// Once per process, and never the token itself: only which client asked for
	// it and who it is meant for.
	v.described.Do(func() {
		slog.Info("verified a token from the identity provider",
			"audience", token.Audience, "azp", c.AuthorizedParty, "expectedClient", v.clientID)
	})

	// Tie the token to this client. The audience would normally do it, but a
	// public client's token carries none, so without this every token the realm
	// signs would be accepted — including those issued to other applications.
	if v.requireAZP {
		if c.AuthorizedParty == "" {
			return nil, errNoAuthorizedParty
		}
		if c.AuthorizedParty != v.clientID {
			return nil, fmt.Errorf("%w: issued to client %q, not %q",
				errWrongAuthorizedParty, c.AuthorizedParty, v.clientID)
		}
	}

	username := c.PreferredUsername
	if username == "" {
		username = c.Name
	}
	if username == "" {
		username = c.Subject
	}

	// No roles: the provider says who this is, the portal says what they may do.
	return &User{
		Username: username,
		Email:    c.Email,
		Groups:   c.Groups,
	}, nil
}

var (
	// errNoAuthorizedParty names the setting, because a provider that omits azp
	// is a supported situation and not a mistake to be puzzled over.
	errNoAuthorizedParty = fmt.Errorf("the token carries no azp claim, so nothing ties it to this " +
		"client; set auth.oidc.requireAuthorizedParty: false if your provider does not issue one")
	errWrongAuthorizedParty = fmt.Errorf("the token belongs to another client")
)
