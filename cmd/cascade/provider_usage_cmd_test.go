// Purpose: CLI-surface tests for `cascade provider test` (the
//   reachability prober) and the credential-redaction acceptance
//   criterion shared by every provider_health_cmd.go/provider_usage_cmd.go
//   subcommand. fakeHTTPDoer substitutes for the real *http.Client via
//   providerDeps.HealthHTTPDoer so TestProviderTest_* never opens a real
//   socket (Art.7.2).
// SPORT: provider · J · S-21 · T-2 (P1-E10-W3-S21-T2).

package main

import (
	"context"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/providers/registry"
	"github.com/acamarata/cascade/internal/secrets"
)

// fakeHTTPDoer is a recording, in-memory httpDoer -- it deliberately never
// imports net/http (see provider_health_cmd.go's httpDoer doc comment for
// why: a _test.go file importing net/http is flagged by
// internal/build's no-network-unit-lane gate regardless of whether it
// ever opens a real socket).
type fakeHTTPDoer struct {
	status int
	err    error
}

func (f fakeHTTPDoer) Get(_ context.Context, _ string) (int, error) {
	if f.err != nil {
		return 0, f.err
	}
	return f.status, nil
}

func TestProviderTest_ReachableSucceeds(t *testing.T) {
	deps := testProviderDeps(t, nil)
	deps.HealthHTTPDoer = fakeHTTPDoer{status: 200}
	seedRegistryProvider(t, deps, sampleRegistryProvider("myclaude", registry.HealthUnknown))

	stdout, _, err := runProvider(t, deps, "test", "myclaude", "--json")
	if err != nil {
		t.Fatalf("provider test: unexpected error: %v", err)
	}
	if !strings.Contains(stdout, `"success": true`) {
		t.Fatalf("expected success=true, got %q", stdout)
	}
}

func TestProviderTest_UnreachableExitsNonZero(t *testing.T) {
	deps := testProviderDeps(t, nil)
	deps.HealthHTTPDoer = fakeHTTPDoer{status: 503}
	seedRegistryProvider(t, deps, sampleRegistryProvider("myclaude", registry.HealthUnknown))

	_, _, err := runProvider(t, deps, "test", "myclaude")
	if err == nil {
		t.Fatal("expected a non-nil error when the provider probe fails")
	}
}

func TestProviderTest_UnknownProviderErrors(t *testing.T) {
	deps := testProviderDeps(t, nil)
	deps.HealthHTTPDoer = fakeHTTPDoer{status: 200}

	_, _, err := runProvider(t, deps, "test", "never-registered")
	if err == nil {
		t.Fatal("expected an error probing a provider the registry has never heard of")
	}
}

// TestProviderCommands_NeverLeakCredential proves the negative the ticket
// requires: with a live credential present in the vault, no provider
// subcommand's output (success or error path) contains it. This test
// would FAIL if a future change echoed rec.AuthRef's resolved value or an
// error wrapped the raw vault contents.
func TestProviderCommands_NeverLeakCredential(t *testing.T) {
	deps := testProviderDeps(t, nil)
	deps.HealthHTTPDoer = fakeHTTPDoer{status: 200}
	const secretValue = "sk-ant-super-secret-value"
	seedRegistryProvider(t, deps, sampleRegistryProvider("leaky-test", registry.HealthHealthy))

	broker, err := providerVaultBroker(deps)
	if err != nil {
		t.Fatalf("providerVaultBroker: %v", err)
	}
	if _, err := broker.Set(context.Background(), "provider.leaky-test.key", []byte(secretValue), secrets.SetUpdate); err != nil {
		t.Fatalf("broker.Set: %v", err)
	}

	for _, args := range [][]string{
		{"list", "--json"}, {"health", "--json"}, {"test", "leaky-test", "--json"}, {"usage", "--json"},
	} {
		stdout, stderr, _ := runProvider(t, deps, args...)
		if strings.Contains(stdout, secretValue) || strings.Contains(stderr, secretValue) {
			t.Fatalf("provider %v leaked the credential value into output", args)
		}
	}

	stdout, stderr, _ := runProvider(t, deps, "remove", "leaky-test", "--json")
	if strings.Contains(stdout, secretValue) || strings.Contains(stderr, secretValue) {
		t.Fatal("provider remove leaked the credential value into output")
	}
}
