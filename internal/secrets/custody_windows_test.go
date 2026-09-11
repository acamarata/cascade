//go:build windows

package secrets

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// TestWindowsHasNoNativeCustody asserts the tier-2 shape: no platform
// backend, so SelectCustody falls through to the encrypted file vault.
func TestWindowsHasNoNativeCustody(t *testing.T) {
	custody, err := platformCustody(Config{Service: "cascade-test"})
	if custody != nil {
		t.Fatal("windows reported a native custody backend")
	}
	if !isKind(err, cascade.KindUnsupported) {
		t.Fatalf("platformCustody = %v, want an unsupported refusal", err)
	}
}

// TestWindowsRefusesElevatedVerbs is the tier-2 acceptance criterion: with
// NO elevation gate configured, the elevated verbs refuse with the
// platform's typed error and never panic, while non-elevated storage
// keeps working against the file vault. broker.go's authorize (see its
// own doc comment) makes an INJECTED gate always decide, on every
// platform — the platform-wide fallback applies only in the no-gate
// case — so this test must not use a gate at all; a version of this test
// wired with an always-allow gate would be asserting a contract broker.go
// never promised, not a Windows quirk (windows-parity-pass-4: this was
// the actual failure — the platform refusal does not override an
// injected gate, by design, matching TestBrokerNilGateRefusesElevatedVerbs
// and TestBrokerGetWithElevation in broker_test.go).
func TestWindowsRefusesElevatedVerbs(t *testing.T) {
	if !isKind(platformElevatedRefusal(), cascade.KindUnsupported) {
		t.Fatalf("platformElevatedRefusal = %v", platformElevatedRefusal())
	}
	custody, err := SelectCustody(Config{Service: "cascade-test", Dir: t.TempDir(), Passphrase: "p"})
	if err != nil {
		t.Fatalf("SelectCustody: %v", err)
	}
	if custody.Name() != fileVaultName {
		t.Fatalf("windows selected %q, want the file vault", custody.Name())
	}
	broker, err := NewBroker(custody, nil)
	if err != nil {
		t.Fatalf("NewBroker: %v", err)
	}
	ctx := context.Background()
	// Storage works: Set and List are not elevated verbs, so they never
	// consult authorize at all.
	if _, err := broker.Set(ctx, "TOKEN", []byte("v"), SetUpdate); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if names, err := broker.List(ctx); err != nil || len(names) != 1 {
		t.Fatalf("List = %v, %v", names, err)
	}
	// Elevated verbs refuse with no gate configured: the platform tier-2
	// refusal applies exactly when authorize has nothing else to consult.
	if _, err := broker.Get(ctx, "TOKEN"); !isKind(err, cascade.KindUnsupported) {
		t.Fatalf("Get on windows = %v, want an unsupported refusal", err)
	}
	if err := broker.Rotate(ctx, "TOKEN", []byte("new")); !isKind(err, cascade.KindUnsupported) {
		t.Fatalf("Rotate on windows = %v, want an unsupported refusal", err)
	}
}
