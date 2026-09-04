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

// TestWindowsRefusesElevatedVerbs is the tier-2 acceptance criterion: the
// elevated verbs refuse with a typed error and never panic, while
// non-elevated storage keeps working against the file vault.
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
	broker, err := NewBroker(custody, &alwaysAllowGate{})
	if err != nil {
		t.Fatalf("NewBroker: %v", err)
	}
	ctx := context.Background()
	// Storage works.
	if _, err := broker.Set(ctx, "TOKEN", []byte("v"), SetUpdate); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if names, err := broker.List(ctx); err != nil || len(names) != 1 {
		t.Fatalf("List = %v, %v", names, err)
	}
	// Elevated verbs refuse, even with a gate that would allow them.
	if _, err := broker.Get(ctx, "TOKEN"); !isKind(err, cascade.KindUnsupported) {
		t.Fatalf("Get on windows = %v, want an unsupported refusal", err)
	}
	if err := broker.Rotate(ctx, "TOKEN", []byte("new")); !isKind(err, cascade.KindUnsupported) {
		t.Fatalf("Rotate on windows = %v, want an unsupported refusal", err)
	}
}

// alwaysAllowGate would authorise anything; the platform refusal must win
// regardless.
type alwaysAllowGate struct{}

func (alwaysAllowGate) Authorize(context.Context, string) error { return nil }
