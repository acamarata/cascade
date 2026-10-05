//go:build !windows

// Purpose: proves `cascade provider reauth <arg>` resolves a status-widget
// ref to the one provider it stands for, and refuses an unknown ref and an
// ambiguous one (an injected hash collision, and a provider literally named
// like another provider's ref) before any custody is selected, any stdin is
// read or anything is written.
// Inputs: reauthDeps (provider_reauth_flagless_test.go: HOME and USERPROFILE
// and custody forced to a temp file vault, a recording keychain runner that
// must never be called, the durable registry) and the durable-state helpers
// of provider_reauth_test.go.
// Outputs: none.
// Constraints: no network (fake intake Doer), no platform keychain; key
// values are assembled at run time. The PII-shaped provider the end-to-end
// cases use is a HOSTNAME, not an email: `provider add` derives the vault name
// provider.<name>.key and the vault refuses '@', so an email-named provider
// cannot be added or re-authorized through the CLI at all (see the build
// report); a hostname passes the vault charset and is still hashed by
// capacity.WidgetRef.
// SPORT: cli.provider.reauth (tests, P1-WID-08).

package main

import (
	"context"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/fleet/capacity"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
)

const (
	reauthHostName  = "gw.example.com"
	reauthEmailName = "user@example.com"
)

// refDeps seeds each named provider with OLD_KEY through `provider add` and
// zeroes the custody and stdin counters so a later refusal can be shown to
// have been first.
func refDeps(t *testing.T, names ...string) (providerDeps, *reauthSpy, string) {
	t.Helper()
	newKey := realisticKey("new")
	deps, spy := reauthDeps(t, map[string]string{"OLD_KEY": realisticKey("old"), "NEW_KEY": newKey}, 200)
	for _, n := range names {
		if _, _, err := runProvider(t, deps, "add", n, "--key-env", "OLD_KEY"); err != nil {
			t.Fatalf("seed add %q: %v", n, err)
		}
	}
	spy.custodies.Store(0)
	spy.reads.Store(0)
	return deps, spy, newKey
}

// credOf returns the credential stored under name's vault reference.
func credOf(t *testing.T, deps providerDeps, name string) string {
	t.Helper()
	rec, _ := durableState(t, deps, name)
	custody, err := deps.NewCustody()
	if err != nil {
		t.Fatalf("custody: %v", err)
	}
	broker, err := secrets.NewBroker(custody, deps.Gate)
	if err != nil {
		t.Fatalf("broker: %v", err)
	}
	v, err := broker.Get(context.Background(), rec.AuthRef.String())
	if err != nil {
		t.Fatalf("Vault.Get for %q: %v", name, err)
	}
	return string(v)
}

func TestProviderReauthResolvesWidgetRef(t *testing.T) {
	deps, _, newKey := refDeps(t, reauthHostName, "plain-key")
	ref := capacity.WidgetRef(reauthHostName)
	if !strings.HasPrefix(ref, "ref-") || ref == reauthHostName {
		t.Fatalf("ref %q is not opaque", ref)
	}
	oldHost, oldPlain := credOf(t, deps, reauthHostName), credOf(t, deps, "plain-key")
	if oldHost == newKey || oldPlain == newKey {
		t.Fatal("the seed credentials already equal the new key: the check below would be vacuous")
	}

	if _, _, err := runProvider(t, deps, "reauth", ref, "--key-env", "NEW_KEY"); err != nil {
		t.Fatalf("reauth by ref: %v", err)
	}
	if got := credOf(t, deps, reauthHostName); got != newKey {
		t.Fatal("the PII-named provider's credential was not replaced through its ref")
	}
	if got := credOf(t, deps, "plain-key"); got != oldPlain {
		t.Fatal("a different provider's credential changed")
	}
	// A plain name is its own ref, and the exact name of a hashed one still works.
	for _, arg := range []string{"plain-key", reauthHostName} {
		if _, _, err := runProvider(t, deps, "reauth", arg, "--key-env", "NEW_KEY"); err != nil {
			t.Fatalf("reauth %q: %v", arg, err)
		}
	}
	if credOf(t, deps, "plain-key") != newKey {
		t.Fatal("reauth by plain name did not replace the credential")
	}
}

func TestProviderReauthUnknownWidgetRef(t *testing.T) {
	deps, spy, _ := refDeps(t, reauthHostName)
	before := credOf(t, deps, reauthHostName)
	spy.custodies.Store(0)
	_, _, err := runProvider(t, deps, "reauth", "ref-000000000000", "--key-env", "NEW_KEY")
	if !cascade.HasKind(err, cascade.KindNotFound) {
		t.Fatalf("unknown ref: %v, want KindNotFound", err)
	}
	assertNothingRead(t, spy, 0)
	if credOf(t, deps, reauthHostName) != before {
		t.Fatal("a refused ref changed a credential")
	}
}

func TestProviderReauthWidgetRefConflicts(t *testing.T) {
	literal := capacity.WidgetRef(reauthHostName) // a provider named exactly like the host provider's ref
	cases := []struct {
		name   string
		names  []string
		arg    string
		refOf  func(string) string
		expect []string
	}{
		{"two names share one ref through an injected hash", []string{"twin-one", "twin-two", "other"}, "ref-aaaaaaaaaaaa",
			func(n string) string {
				if strings.HasPrefix(n, "twin-") {
					return "ref-aaaaaaaaaaaa"
				}
				return capacity.WidgetRef(n)
			}, []string{"twin-one", "twin-two"}},
		{"a provider named like another provider's ref", []string{reauthHostName, literal}, literal, capacity.WidgetRef,
			[]string{reauthHostName, literal}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deps, spy, _ := refDeps(t, tc.names...)
			before := map[string]string{}
			for _, n := range tc.names {
				before[n] = credOf(t, deps, n)
			}
			spy.custodies.Store(0)
			prev := reauthRefOf
			reauthRefOf = tc.refOf
			defer func() { reauthRefOf = prev }()

			_, _, err := runProvider(t, deps, "reauth", tc.arg, "--key-env", "NEW_KEY")
			if !cascade.HasKind(err, cascade.KindConflict) {
				t.Fatalf("ambiguous arg: %v, want KindConflict", err)
			}
			for _, n := range tc.expect {
				if !strings.Contains(err.Error(), `"`+n+`"`) {
					t.Errorf("conflict %q does not name %q", err, n)
				}
			}
			assertNothingRead(t, spy, 0)
			for _, n := range tc.names {
				if credOf(t, deps, n) != before[n] {
					t.Errorf("a refused conflict changed %q's credential", n)
				}
			}
		})
	}
}

// TestPickReauthTargetRule is the resolution rule on its own, including an
// email name (which no CLI path can add, but a registry can hold).
func TestPickReauthTargetRule(t *testing.T) {
	refOf := capacity.WidgetRef
	names := []string{"b", "a", reauthEmailName}
	if got, err := pickReauthTarget("a", names, refOf); err != nil || got != "a" {
		t.Errorf("exact name = %q, %v", got, err)
	}
	if got, err := pickReauthTarget(refOf(reauthEmailName), names, refOf); err != nil || got != reauthEmailName {
		t.Errorf("email ref = %q, %v", got, err)
	}
	if _, err := pickReauthTarget("zzz", nil, refOf); !cascade.HasKind(err, cascade.KindNotFound) {
		t.Errorf("no providers = %v, want KindNotFound", err)
	}
}
