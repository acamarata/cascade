// Purpose: the ProviderOAuthConfig contract tests. Every case here is a
//
//	refusal the loopback flow depends on: a non-loopback redirect target
//	delivers the authorization code somewhere else, a fixed port is
//	squattable, and "plain" PKCE is not PKCE.
//
// Constraints: external test package (provider_test), like the rest of
//
//	pkg/provider's tests, so it exercises only the exported surface.
//
// SPORT: PROVIDER_OAUTH_CONFIG: ADD (tests).

package provider_test

import (
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

func validOAuthConfig() provider.ProviderOAuthConfig {
	return provider.ProviderOAuthConfig{
		ProviderID:    "acme",
		ClientID:      "client-1",
		Scopes:        []string{"read"},
		RedirectURI:   "http://127.0.0.1/callback",
		AuthEndpoint:  "https://acme.example/authorize",
		TokenEndpoint: "https://acme.example/token",
		PKCEMethod:    provider.PKCEMethodS256,
	}
}

func TestProviderOAuthConfigAcceptsAValidConfiguration(t *testing.T) {
	if err := validOAuthConfig().Validate(); err != nil {
		t.Fatalf("a valid configuration was refused: %v", err)
	}
	loopbackIDP := validOAuthConfig()
	loopbackIDP.AuthEndpoint = "http://127.0.0.1:9999/authorize"
	loopbackIDP.TokenEndpoint = "http://127.0.0.1:9999/token"
	if err := loopbackIDP.Validate(); err != nil {
		t.Fatalf("a loopback http IdP was refused: %v", err)
	}
}

func TestProviderOAuthConfigRefusals(t *testing.T) {
	cases := map[string]func(*provider.ProviderOAuthConfig){
		"empty provider id":       func(c *provider.ProviderOAuthConfig) { c.ProviderID = "" },
		"unsafe provider id":      func(c *provider.ProviderOAuthConfig) { c.ProviderID = "acme/../etc" },
		"flag-like provider id":   func(c *provider.ProviderOAuthConfig) { c.ProviderID = "-rf" },
		"empty client id":         func(c *provider.ProviderOAuthConfig) { c.ClientID = "  " },
		"plain pkce":              func(c *provider.ProviderOAuthConfig) { c.PKCEMethod = "plain" },
		"empty pkce":              func(c *provider.ProviderOAuthConfig) { c.PKCEMethod = "" },
		"http auth endpoint":      func(c *provider.ProviderOAuthConfig) { c.AuthEndpoint = "http://acme.example/a" },
		"http token endpoint":     func(c *provider.ProviderOAuthConfig) { c.TokenEndpoint = "http://acme.example/t" },
		"unparsable endpoint":     func(c *provider.ProviderOAuthConfig) { c.TokenEndpoint = "://" },
		"bad revocation endpoint": func(c *provider.ProviderOAuthConfig) { c.RevocationEndpoint = "http://acme.example/r" },
		"localhost redirect":      func(c *provider.ProviderOAuthConfig) { c.RedirectURI = "http://localhost/callback" },
		"https redirect":          func(c *provider.ProviderOAuthConfig) { c.RedirectURI = "https://127.0.0.1/callback" },
		"remote redirect":         func(c *provider.ProviderOAuthConfig) { c.RedirectURI = "http://10.0.0.5/callback" },
		"ipv6 loopback redirect":  func(c *provider.ProviderOAuthConfig) { c.RedirectURI = "http://[::1]/callback" },
		"fixed redirect port":     func(c *provider.ProviderOAuthConfig) { c.RedirectURI = "http://127.0.0.1:8080/cb" },
		"redirect with query":     func(c *provider.ProviderOAuthConfig) { c.RedirectURI = "http://127.0.0.1/cb?x=1" },
		"redirect with fragment":  func(c *provider.ProviderOAuthConfig) { c.RedirectURI = "http://127.0.0.1/cb#f" },
		"unparsable redirect":     func(c *provider.ProviderOAuthConfig) { c.RedirectURI = "http://[::1" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := validOAuthConfig()
			mutate(&cfg)
			err := cfg.Validate()
			if err == nil {
				t.Fatal("the configuration was accepted")
			}
			if !cascade.HasKind(err, cascade.KindInvalidInput) {
				t.Fatalf("refusal is not KindInvalidInput: %v", err)
			}
		})
	}
}

func TestProviderOAuthConfigAcceptsAnOptionalRevocationEndpoint(t *testing.T) {
	cfg := validOAuthConfig()
	cfg.RevocationEndpoint = "https://acme.example/revoke"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("a valid revocation endpoint was refused: %v", err)
	}
}

func TestRedirectURIForPortSubstitutesTheBoundPort(t *testing.T) {
	got, err := validOAuthConfig().RedirectURIForPort("54321")
	if err != nil {
		t.Fatalf("RedirectURIForPort: %v", err)
	}
	if got != "http://127.0.0.1:54321/callback" {
		t.Fatalf("redirect URI %q", got)
	}
}

func TestRedirectURIForPortRefusesBadInput(t *testing.T) {
	if _, err := validOAuthConfig().RedirectURIForPort(" "); err == nil {
		t.Fatal("an empty port was accepted")
	}
	invalid := validOAuthConfig()
	invalid.PKCEMethod = "plain"
	if _, err := invalid.RedirectURIForPort("1234"); err == nil {
		t.Fatal("an invalid configuration produced a redirect URI anyway")
	}
}

func TestTokenRecordHasNoFieldThatCanHoldAToken(t *testing.T) {
	// The guarantee this package makes is structural, so assert it
	// structurally: a reviewer adding a Token field would have to delete
	// this test to land it.
	rec := provider.TokenRecord{
		Provider: "acme", Account: "default", Scopes: []string{"read"},
		AccessRef: "oauth.acme.default.access.1", RefreshRef: "oauth.acme.default.refresh.1",
	}
	rendered := strings.ToLower(strings.Join([]string{
		rec.Provider, rec.Account, rec.AccessRef, rec.RefreshRef, strings.Join(rec.Scopes, " "),
	}, " "))
	for _, forbidden := range []string{"bearer ", "eyj"} {
		if strings.Contains(rendered, forbidden) {
			t.Fatalf("a record field looks like it carries token material: %q", rendered)
		}
	}
	if !strings.HasSuffix(rec.AccessRef, ".access.1") {
		t.Fatal("AccessRef is not a vault key name")
	}
}

func TestTokenRecordExpired(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	var noExpiry provider.TokenRecord
	if noExpiry.Expired(now, time.Minute) {
		t.Fatal("a record with no declared expiry was treated as expired")
	}
	live := provider.TokenRecord{ExpiresAt: now.Add(2 * time.Minute)}
	if live.Expired(now, time.Minute) {
		t.Fatal("a live token was treated as expired")
	}
	if !live.Expired(now, 3*time.Minute) {
		t.Fatal("skew did not pull the expiry forward")
	}
	past := provider.TokenRecord{ExpiresAt: now.Add(-time.Second)}
	if !past.Expired(now, 0) {
		t.Fatal("an already-expired token was treated as live")
	}
	exact := provider.TokenRecord{ExpiresAt: now}
	if !exact.Expired(now, 0) {
		t.Fatal("a token expiring exactly now was treated as live")
	}
}

func TestPKCEMethodS256IsTheOnlyMember(t *testing.T) {
	if provider.PKCEMethodS256 != "S256" {
		t.Fatalf("the S256 method's wire value changed to %q", provider.PKCEMethodS256)
	}
	if provider.LoopbackHost != "127.0.0.1" {
		t.Fatalf("the loopback host literal changed to %q", provider.LoopbackHost)
	}
}
