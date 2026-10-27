// Purpose: the reserved daemon key names refuse every vault verb that could
//
//	set, rename onto, replace or delete them: set (all modes), rotate,
//	delete, vault.env import and backup import. An ordinary name still works,
//	so the refusals are about the name and not a broken broker.
//
// SPORT: internal/secrets reserved-name tests (P1-SEC-23).

package secrets

import (
	"bytes"
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func reservedRefusal(t *testing.T, what string, err error) {
	t.Helper()
	if !cascade.HasKind(err, cascade.KindPermissionDenied) || !errors.Is(err, ErrReservedName) ||
		!strings.Contains(err.Error(), "reserved daemon key") {
		t.Fatalf("%s = %v, want the reserved-name refusal", what, err)
	}
}

func TestVaultSetRefusesReservedApprovalKey(t *testing.T) {
	ctx := context.Background()
	for _, name := range daemonKeyNames {
		fv, err := newFileVaultCustody(Config{Service: DefaultVaultService, Dir: t.TempDir(), Passphrase: "fake-reserved-pass"})
		if err != nil {
			t.Fatal(err)
		}
		b, err := NewBroker(fv, &allowGate{})
		if err != nil {
			t.Fatal(err)
		}
		original := []byte("fake-original-daemon-key-bytes")
		if err := fv.Set(ctx, name, original); err != nil {
			t.Fatal(err)
		}
		for _, mode := range []SetMode{SetUpdate, SetRename, SetRefuse} {
			_, err := b.Set(ctx, name, []byte("fake-replacement"), mode)
			reservedRefusal(t, name+" vault set", err)
		}
		reservedRefusal(t, name+" vault rotate", b.Rotate(ctx, name, []byte("fake-replacement")))
		reservedRefusal(t, name+" vault delete", b.Delete(ctx, name))
		_, err = Import(ctx, b, []byte(name+"=fake-replacement\n"))
		reservedRefusal(t, name+" vault import", err)
		if got, _ := fv.Get(ctx, name); !bytes.Equal(got, original) {
			t.Fatalf("%s changed after refused verbs", name)
		}
		// The control: a neighbouring ordinary name is writable and deletable.
		if _, err := b.Set(ctx, name+"_2", []byte("fake"), SetUpdate); err != nil {
			t.Fatalf("ordinary set next to %s: %v", name, err)
		}
		if err := b.Delete(ctx, name+"_2"); err != nil {
			t.Fatalf("ordinary delete next to %s: %v", name, err)
		}
	}
}

// TestReservedNameRefusedInRenameMode drives Set's rename path: with the
// reserved name taken, SetRename would otherwise store the value beside it
// under a suffixed name. (A rename can never land ON a reserved name:
// freeName only appends _<n>, and no reserved name has that form.)
func TestReservedNameRefusedInRenameMode(t *testing.T) {
	ctx := context.Background()
	b, cc := newCountingBroker(t)
	if err := cc.Custody.Set(ctx, ApprovalKeyName, []byte("fake-daemon-key")); err != nil {
		t.Fatal(err)
	}
	before := cc.sets.Load()
	_, err := b.Set(ctx, ApprovalKeyName, []byte("fake-replacement"), SetRename)
	reservedRefusal(t, "set in rename mode", err)
	if names, _ := cc.List(ctx); len(names) != 1 || cc.sets.Load() != before {
		t.Fatalf("rename mode wrote beside the reserved key: %v", names)
	}
	if res, err := b.Set(ctx, "FAKE_ORDINARY", []byte("v"), SetRename); err != nil || res.Name != "FAKE_ORDINARY" {
		t.Fatalf("control rename-mode set = %+v, %v", res, err)
	}
}

func TestReservedDaemonKeyNeverRehydratedOrEgressListed(t *testing.T) {
	ctx := context.Background()
	b, cc := newCountingBroker(t)
	if err := cc.Custody.Set(ctx, ApprovalKeyName, []byte("fake-daemon-key-material")); err != nil {
		t.Fatal(err)
	}
	if err := cc.Custody.Set(ctx, "FAKE_API_KEY", []byte("fake-value")); err != nil {
		t.Fatal(err)
	}
	r, err := NewRehydrator(b)
	if err != nil {
		t.Fatal(err)
	}
	rc, err := r.Rehydrate(ctx, []byte("send <apikey>"+ApprovalKeyName+"</apikey>"))
	// Tag names are upper-case, so the reserved name cannot even be tagged;
	// the refusal must hold either way and nothing is emitted.
	if err == nil || rc != nil {
		t.Fatalf("rehydrating the approval key = %v, %v; want a refusal", rc, err)
	}
	if rc, err := r.Rehydrate(ctx, []byte("send <apikey>FAKE_API_KEY</apikey>")); err != nil || !bytes.Contains(rc.Data, []byte("fake-value")) {
		t.Fatalf("control rehydration = %v, %v", rc, err)
	}
	ev, err := NewEgressVault(b)
	if err != nil {
		t.Fatal(err)
	}
	names, err := ev.List(ctx)
	if err != nil || len(names) != 1 || names[0] != "FAKE_API_KEY" {
		t.Fatalf("egress vault names = %v, %v; want only the ordinary entry", names, err)
	}
	_, err = ev.Get(ctx, ApprovalKeyName)
	reservedRefusal(t, "egress get", err)
	if _, _, err := (&ApprovalKeyReader{broker: b}).ApprovalKey(ctx); !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("the approval key reader must still reach the reserved key (malformed here): %v", err)
	}
}

// TestNoReservedNameIsARenameTarget: Set's rename mode stores under
// name_<n> (freeName), so a reserved name of that form could be reached as a
// rename target without ever being named. None may have it.
func TestNoReservedNameIsARenameTarget(t *testing.T) {
	if len(reservedDaemonKeyNames) == 0 {
		t.Fatal("no reserved names; this test proves nothing")
	}
	suffix := regexp.MustCompile(`_[0-9]+$`)
	for _, name := range reservedDaemonKeyNames {
		if suffix.MatchString(name) {
			t.Fatalf("reserved name %q has the rename-target form name_<n>", name)
		}
		if !reservedDaemonKey(name) {
			t.Fatalf("reservedDaemonKey(%q) = false", name)
		}
	}
}
