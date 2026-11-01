//go:build !windows

// Purpose: unit tests of the gateway branch's small decisions: credential
// routing between the call-time source and the base source, the record
// checks before a driver is built, and the header-style to auth-mode map.
// Constraints: no network, no vault, no real credentials.
// SPORT: internal/providers/dispatch gateway/ADD.

package dispatch

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/internal/providers/gateway"
	"github.com/acamarata/cascade/internal/providers/registry"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/providers/anthropic"
)

// stubCredentials is a CredentialSource answering one fixed value.
type stubCredentials struct{ value string }

func (s stubCredentials) Resolve(context.Context, string) (string, error) { return s.value, nil }

func TestGatewayCredentialsRouteNonGatewayRefs(t *testing.T) {
	_, err := gatewayCredentials{}.Resolve(context.Background(), "vault:other")
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("no base source: %v, want unavailable", err)
	}
	got, err := gatewayCredentials{base: stubCredentials{value: "from-base"}}.Resolve(context.Background(), "vault:other")
	if err != nil || got != "from-base" {
		t.Fatalf("non-gateway ref = %q, %v; want the base source's value", got, err)
	}
	_, err = gatewayCredentials{base: stubCredentials{value: "x"}}.Resolve(context.Background(), gatewayRefPrefix+"missing")
	if !cascade.HasKind(err, cascade.KindPermissionDenied) {
		t.Fatalf("unadmitted account: %v, want permission-denied", err)
	}
}

func TestAdmittedGatewayRefusals(t *testing.T) {
	acct := gateway.Account{Name: "alpha", BaseURL: "https://gateway.invalid", Header: gateway.HeaderBearer}
	r := &Resolver{gateways: []gateway.Account{acct}}
	good := registry.ProviderRecord{Name: "gw", Driver: registry.DriverAnthropic, BaseURL: acct.BaseURL, AuthRef: registry.VaultKeyRef("gateway:alpha")}
	for name, mutate := range map[string]func(*registry.ProviderRecord){
		"wrong driver":  func(rec *registry.ProviderRecord) { rec.Driver = "openai" },
		"malformed ref": func(rec *registry.ProviderRecord) { rec.AuthRef = registry.VaultKeyRef("vault:alpha") },
	} {
		rec := good
		mutate(&rec)
		if _, err := r.admittedGateway(rec); !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Fatalf("%s: %v, want invalid-input", name, err)
		}
	}
	if _, err := r.admittedGateway(good); err != nil {
		t.Fatalf("well-formed record refused: %v", err)
	}
}

func TestGatewayAuthModeMapsHeaderStyle(t *testing.T) {
	for style, want := range map[gateway.HeaderStyle]anthropic.AuthMode{
		gateway.HeaderBearer:  anthropic.AuthModeBearerKey,
		gateway.HeaderXAPIKey: anthropic.AuthModeKey,
	} {
		if got, err := gatewayAuthMode(gateway.Account{Name: "alpha", Header: style}); err != nil || got != want {
			t.Fatalf("style %q = %v, %v; want %v", style, got, err, want)
		}
	}
	if _, err := gatewayAuthMode(gateway.Account{Name: "alpha", Header: "bogus"}); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("unsupported style: %v, want invalid-input", err)
	}
}
