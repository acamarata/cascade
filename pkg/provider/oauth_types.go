// Purpose: the public OAuth contract every provider driver consumes -
//
//	ProviderOAuthConfig (what a provider's authorization server looks
//	like), TokenRecord (what cascade remembers about a granted
//	credential), and the OAuthBroker interface the loopback PKCE broker
//	in internal/secrets implements. Declared in pkg/ so providers/ and
//	plugins/ can consume it without importing internal/
//	(02-TARGET-STRUCTURE.md §import-boundaries, Art.10.2).
//
// Inputs: values a provider driver supplies at registration time. Nothing
//
//	here is read from the environment, a file, or the network.
//
// Outputs: a validated configuration, plus a token record that names two
//
//	VAULT KEYS rather than carrying token bytes.
//
// Constraints: TokenRecord has no field that can hold a token, by
//
//	construction. AccessRef and RefreshRef are vault broker key NAMES,
//	so a TokenRecord may be logged, serialized into a diagnostic, or
//	returned across an RPC boundary without disclosing a bearer
//	credential - the same rule internal/secrets' detector findings
//	follow, and for the same reason: a type that cannot carry a secret
//	turns "do not log the token" from a review finding into a compile
//	error. Validate is fail-closed: a redirect target it cannot prove is
//	loopback is refused, never normalised.
//
// SPORT: PROVIDER_OAUTH_CONFIG: ADD (pkg/provider.ProviderOAuthConfig,
//
//	OAuthBroker interface).

package provider

import (
	"context"
	"net/url"
	"strings"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// PKCEMethod names the RFC 7636 code-challenge transformation. The
// enumeration has exactly one member on purpose: "plain" sends the
// verifier itself as the challenge, which defeats the whole mechanism the
// moment an attacker can read the authorization request, and cascade
// therefore does not offer it.
type PKCEMethod string

// PKCEMethodS256 is the SHA-256 challenge transformation of RFC 7636 §4.2,
// the only method cascade sends or accepts.
const PKCEMethodS256 PKCEMethod = "S256"

// LoopbackHost is the only redirect host cascade accepts: the IPv4 loopback
// literal. "localhost" is deliberately NOT accepted - it is a name, and a
// poisoned resolver or a hosts-file entry can point it at an address that
// is not this machine, which would hand the authorization code to whoever
// answers there.
const LoopbackHost = "127.0.0.1"

// ProviderOAuthConfig describes one provider's authorization server and the
// client cascade registers with it. It carries no secret: a public OAuth
// client using PKCE has no client secret to carry, which is precisely why
// PKCE exists.
//
// The name is fixed by the ticket contract (P1-E08-W2-S15-T2 and
// 06-FORGE-SPEC §5.23 both name pkg/provider.ProviderOAuthConfig), and Wave
// 3's driver tickets are specified against it. revive's stutter rule would
// have this be provider.OAuthConfig; renaming a contract type to satisfy a
// style lint is the wrong trade when the interface it pairs with
// (OAuthBroker) does not stutter and the two ship together.
//
//nolint:revive // contract-fixed name; see the paragraph above
type ProviderOAuthConfig struct {
	// ProviderID names the provider this configuration belongs to. It is
	// part of every vault key the broker writes, so it is restricted to
	// the vault's own name charset (letters, digits, '_', '-', '.').
	ProviderID string
	// ClientID is the public OAuth client identifier. Not a secret.
	ClientID string
	// Scopes are the scopes requested at authorization time.
	Scopes []string
	// RedirectURI is the loopback redirect target, written WITHOUT a port:
	// the broker binds an OS-assigned ephemeral port and substitutes it,
	// because a hard-coded port either collides with another process or
	// lets one squat the callback. Example: "http://127.0.0.1/callback".
	RedirectURI string
	// AuthEndpoint is the authorization endpoint the browser is sent to.
	AuthEndpoint string
	// TokenEndpoint is the endpoint the authorization code and refresh
	// token are exchanged at.
	TokenEndpoint string
	// RevocationEndpoint is the optional RFC 7009 endpoint Revoke posts to.
	// When empty, Revoke forgets the credential locally and says so rather
	// than claiming a server-side revocation it did not perform.
	RevocationEndpoint string
	// PKCEMethod must be PKCEMethodS256.
	PKCEMethod PKCEMethod
	// NoBrowser suppresses the browser launch and prints the authorization
	// URL to the broker's diagnostics stream instead, still waiting for the
	// callback. It is the field behind `provider add --oauth --no-browser`
	// (R-16.47); the CLI flag itself is declared by J/S-20.T1.
	NoBrowser bool
}

// nameCharsetOK reports whether s uses only the vault's secret-name
// charset. Duplicated as a predicate here rather than imported from
// internal/secrets: pkg/ may not import internal/ (Art.10.2), and the
// broker re-validates every name it writes anyway - this copy exists so a
// bad ProviderID is refused at the contract boundary instead of surfacing
// later as an opaque vault error.
func nameCharsetOK(s string) bool {
	if s == "" || strings.HasPrefix(s, "-") {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '_', r == '-', r == '.':
		default:
			return false
		}
	}
	return true
}

// endpointOK reports whether raw is an acceptable endpoint URL: https
// anywhere, or http restricted to the loopback literal (which is what a
// test IdP and a loopback-only development server look like). Plain http to
// a remote host would put the authorization code and the tokens on the
// wire in clear text.
func endpointOK(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return false
	}
	if u.Scheme == "https" {
		return true
	}
	return u.Scheme == "http" && u.Hostname() == LoopbackHost
}

// Validate reports whether the configuration is usable, refusing anything
// it cannot positively accept. Every refusal is a KindInvalidInput taxonomy
// error naming the field, never a normalised value: silently rewriting a
// redirect target is how a flow ends up delivering its code somewhere the
// operator did not choose.
func (c ProviderOAuthConfig) Validate() error {
	switch {
	case !nameCharsetOK(c.ProviderID):
		return cascade.Newf(cascade.KindInvalidInput,
			"provider: oauth provider_id %q must be non-empty and use only letters, digits, '_', '-' or '.'", c.ProviderID)
	case strings.TrimSpace(c.ClientID) == "":
		return cascade.New(cascade.KindInvalidInput, "provider: oauth client_id must not be empty")
	case c.PKCEMethod != PKCEMethodS256:
		return cascade.Newf(cascade.KindInvalidInput,
			"provider: oauth pkce_method must be %q; %q (including \"plain\") is refused", PKCEMethodS256, c.PKCEMethod)
	case !endpointOK(c.AuthEndpoint):
		return cascade.Newf(cascade.KindInvalidInput,
			"provider: oauth auth_endpoint %q must be https, or http on %s", c.AuthEndpoint, LoopbackHost)
	case !endpointOK(c.TokenEndpoint):
		return cascade.Newf(cascade.KindInvalidInput,
			"provider: oauth token_endpoint %q must be https, or http on %s", c.TokenEndpoint, LoopbackHost)
	case c.RevocationEndpoint != "" && !endpointOK(c.RevocationEndpoint):
		return cascade.Newf(cascade.KindInvalidInput,
			"provider: oauth revocation_endpoint %q must be https, or http on %s", c.RevocationEndpoint, LoopbackHost)
	}
	return c.validateRedirect()
}

// validateRedirect enforces the loopback redirect rule: http on the 127.0.0.1
// literal, with no port (the broker supplies the ephemeral one) and no query
// or fragment (which would collide with the authorization response's own
// code and state parameters).
func (c ProviderOAuthConfig) validateRedirect() error {
	u, err := url.Parse(c.RedirectURI)
	if err != nil {
		return cascade.Wrapf(cascade.KindInvalidInput, err,
			"provider: oauth redirect_uri %q is not a URL", c.RedirectURI)
	}
	switch {
	case u.Scheme != "http" || u.Hostname() != LoopbackHost:
		return cascade.Newf(cascade.KindInvalidInput,
			"provider: oauth redirect_uri %q must be http on the %s literal (a name such as \"localhost\" can resolve off-host)",
			c.RedirectURI, LoopbackHost)
	case u.Port() != "":
		return cascade.Newf(cascade.KindInvalidInput,
			"provider: oauth redirect_uri %q must not fix a port; the broker binds an ephemeral one", c.RedirectURI)
	case u.RawQuery != "" || u.Fragment != "":
		return cascade.Newf(cascade.KindInvalidInput,
			"provider: oauth redirect_uri %q must carry no query or fragment", c.RedirectURI)
	}
	return nil
}

// RedirectURIForPort returns the redirect URI with the broker's bound port
// substituted. Validate must have passed first; a configuration that has
// not been validated returns a KindInvalidInput error rather than a
// best-effort string.
func (c ProviderOAuthConfig) RedirectURIForPort(port string) (string, error) {
	if err := c.Validate(); err != nil {
		return "", err
	}
	if strings.TrimSpace(port) == "" {
		return "", cascade.New(cascade.KindInvalidInput, "provider: oauth redirect port must not be empty")
	}
	u, err := url.Parse(c.RedirectURI)
	if err != nil {
		return "", cascade.Wrapf(cascade.KindInvalidInput, err, "provider: oauth redirect_uri %q is not a URL", c.RedirectURI)
	}
	u.Host = LoopbackHost + ":" + port
	return u.String(), nil
}

// TokenRecord is what cascade remembers about one granted credential. It
// names two vault keys; it never holds token bytes, and there is no field
// it could hold them in. That is the point: a TokenRecord is safe in a log
// line, a diagnostic, or an RPC response, so no caller has to remember a
// redaction rule.
type TokenRecord struct {
	// Provider is the ProviderOAuthConfig.ProviderID this grant belongs to.
	Provider string `json:"provider"`
	// Account is the local account label the grant is filed under.
	Account string `json:"account"`
	// Scopes are the scopes the authorization server actually granted,
	// which may be narrower than the scopes requested.
	Scopes []string `json:"scopes"`
	// AccessRef is the vault key holding the access token.
	AccessRef string `json:"access_ref"`
	// RefreshRef is the vault key holding the refresh token, or "" when the
	// authorization server issued no refresh token.
	RefreshRef string `json:"refresh_ref"`
	// ExpiresAt is when the access token stops being usable. The zero value
	// means the authorization server declared no expiry.
	ExpiresAt time.Time `json:"exp"`
}

// Expired reports whether the access token must not be used at now, with
// skew subtracted so a token that expires mid-request is treated as already
// expired. A zero ExpiresAt is never expired: the server declared no
// lifetime, and inventing one here would refuse a valid credential.
func (r TokenRecord) Expired(now time.Time, skew time.Duration) bool {
	if r.ExpiresAt.IsZero() {
		return false
	}
	return !now.Before(r.ExpiresAt.Add(-skew))
}

// OAuthBroker runs the loopback PKCE flow for one provider configuration.
// internal/secrets implements it; the Anthropic and Gemini drivers consume
// it in Wave 3 (J/S-19.T2, J/S-19.T4). No concrete provider is registered
// against this interface here.
type OAuthBroker interface {
	// Start runs a full authorization: it binds a loopback listener, sends
	// the operator's browser to the authorization endpoint with a fresh
	// PKCE challenge and state, waits for the redirect, and exchanges the
	// code. It returns the stored record, never the tokens.
	Start(ctx context.Context) (TokenRecord, error)
	// Refresh exchanges the stored refresh token for a new access token.
	// Concurrent calls for one account collapse into a single outbound
	// request; every caller receives that request's result or its error.
	Refresh(ctx context.Context, account string) (TokenRecord, error)
	// Revoke forgets the credential. It posts to RevocationEndpoint when
	// one is configured, and removes the vault entries either way.
	Revoke(ctx context.Context, account string) error
}
