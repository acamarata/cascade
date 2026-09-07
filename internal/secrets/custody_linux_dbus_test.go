//go:build linux

// Purpose: exercises secretServiceCustody's live-bus paths (open, unlock,
//
//	attrs, search, close, and the Set/Get/Delete/List/Available verbs)
//	against the fake secret-service peer in custody_linux_fake_test.go,
//	the only way to drive these without a real session bus. The unreachable
//	and validation paths already covered in custody_linux_test.go are not
//	repeated here.
//
// Inputs: none beyond the fake service's scripted fields per test.
// Outputs: n/a (test file).
// Constraints: every test uses t.TempDir()/an in-memory fake, never a real
//
//	bus or the real $HOME.
//
// SPORT: internal/secrets Custody/TEST (linux secret-service fake peer,
//
//	R-14 CI coverage gap).

package secrets

import (
	"bytes"
	"context"
	"testing"

	"github.com/godbus/dbus/v5"

	"github.com/acamarata/cascade/pkg/cascade"
)

func newTestSecretService(t *testing.T, fs *fakeSecretService) *secretServiceCustody {
	t.Helper()
	return &secretServiceCustody{service: "cascade-test", connect: fs.connect(t)}
}

// TestSecretServiceRoundTripsOverFakeBus drives Set/Get/List/Delete through
// a full session open/unlock/search/close cycle, proving those halves
// behave correctly against a real (if fake) peer, not just against the
// unreachable-bus refusal.
func TestSecretServiceRoundTripsOverFakeBus(t *testing.T) {
	fs := newFakeSecretService()
	s := newTestSecretService(t, fs)
	ctx := context.Background()

	if !s.Available() {
		t.Fatal("Available() = false against a fake bus that owns the name")
	}
	if err := s.Set(ctx, "A", []byte("secret-a")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, err := s.Get(ctx, "A")
	if err != nil || !bytes.Equal(got, []byte("secret-a")) {
		t.Fatalf("Get = %q, %v", got, err)
	}
	names, err := s.List(ctx)
	if err != nil || len(names) != 1 || names[0] != "A" {
		t.Fatalf("List = %v, %v", names, err)
	}
	if err := s.Delete(ctx, "A"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Get(ctx, "A"); !isKind(err, cascade.KindNotFound) {
		t.Fatalf("Get after delete = %v, want not-found", err)
	}
}

// TestSecretServiceAvailableFalseWithoutOwner covers the branch of
// Available() that reaches the bus but finds the well-known name unowned.
func TestSecretServiceAvailableFalseWithoutOwner(t *testing.T) {
	fs := newFakeSecretService()
	fs.unowned = true
	s := newTestSecretService(t, fs)
	if s.Available() {
		t.Fatal("Available() = true with no name owner")
	}
}

// TestSecretServiceRefusesLockedCollection covers unlock()'s fail-closed
// branch: Unlock succeeds at the protocol level but reports nothing
// unlocked, which must refuse rather than proceed on a half-open session.
func TestSecretServiceRefusesLockedCollection(t *testing.T) {
	fs := newFakeSecretService()
	fs.locked = true
	s := newTestSecretService(t, fs)
	if err := s.Set(context.Background(), "A", []byte("v")); !isKind(err, cascade.KindUnavailable) {
		t.Fatalf("Set = %v, want unavailable", err)
	}
}

// TestSecretServiceOpenSessionFailure covers open()'s OpenSession-call
// failure branch.
func TestSecretServiceOpenSessionFailure(t *testing.T) {
	fs := newFakeSecretService()
	fs.failOpenSession = true
	s := newTestSecretService(t, fs)
	if _, err := s.List(context.Background()); !isKind(err, cascade.KindUnavailable) {
		t.Fatalf("List = %v, want unavailable", err)
	}
}

// TestSecretServiceSearchCallFailure covers search()'s own Call failure,
// distinct from a locked-items result.
func TestSecretServiceSearchCallFailure(t *testing.T) {
	fs := newFakeSecretService()
	fs.failSearch = true
	s := newTestSecretService(t, fs)
	if _, err := s.Get(context.Background(), "A"); !isKind(err, cascade.KindUnavailable) {
		t.Fatalf("Get = %v, want unavailable", err)
	}
}

// TestSecretServiceLockedItemsRefused covers search()'s "matches exist but
// are all locked" branch, distinct from OpenSession/Unlock failures.
func TestSecretServiceLockedItemsRefused(t *testing.T) {
	fs := newFakeSecretService()
	fs.items = append(fs.items, &fakeSecretItem{
		path:   lockedItemPath,
		attrs:  map[string]string{ssAttrService: "cascade-test", ssAttrName: "A"},
		locked: true,
	})
	s := newTestSecretService(t, fs)
	if _, err := s.Get(context.Background(), "A"); !isKind(err, cascade.KindUnavailable) {
		t.Fatalf("Get = %v, want unavailable", err)
	}
}

// TestSecretServiceCorruptAttributesOnList covers List()'s type-assertion
// refusal when an item's Attributes property is not a string map.
func TestSecretServiceCorruptAttributesOnList(t *testing.T) {
	fs := newFakeSecretService()
	fs.corruptAttrs = true
	s := newTestSecretService(t, fs)
	if err := s.Set(context.Background(), "A", []byte("v")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if _, err := s.List(context.Background()); !isKind(err, cascade.KindIntegrity) {
		t.Fatalf("List = %v, want integrity", err)
	}
}

// TestSecretServiceCreateItemFailure covers Set()'s CreateItem-call
// failure branch.
func TestSecretServiceCreateItemFailure(t *testing.T) {
	fs := newFakeSecretService()
	fs.failCreate = true
	s := newTestSecretService(t, fs)
	if err := s.Set(context.Background(), "A", []byte("v")); !isKind(err, cascade.KindUnavailable) {
		t.Fatalf("Set = %v, want unavailable", err)
	}
}

// TestSecretServiceGetSecretCallFailure covers Get()'s GetSecret-call
// failure branch, once the item is found by search.
func TestSecretServiceGetSecretCallFailure(t *testing.T) {
	fs := newFakeSecretService()
	fs.failGetSecret = true
	s := newTestSecretService(t, fs)
	if err := s.Set(context.Background(), "A", []byte("v")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if _, err := s.Get(context.Background(), "A"); !isKind(err, cascade.KindUnavailable) {
		t.Fatalf("Get = %v, want unavailable", err)
	}
}

// TestSecretServiceDeleteMissingAndCallFailure covers Delete()'s
// not-found branch (search finds nothing) and its Delete-call failure
// branch (search finds the item, the delete itself fails).
func TestSecretServiceDeleteMissingAndCallFailure(t *testing.T) {
	fs := newFakeSecretService()
	s := newTestSecretService(t, fs)
	if err := s.Delete(context.Background(), "missing"); !isKind(err, cascade.KindNotFound) {
		t.Fatalf("Delete = %v, want not-found", err)
	}

	fs2 := newFakeSecretService()
	fs2.failDelete = true
	s2 := newTestSecretService(t, fs2)
	if err := s2.Set(context.Background(), "A", []byte("v")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := s2.Delete(context.Background(), "A"); !isKind(err, cascade.KindUnavailable) {
		t.Fatalf("Delete = %v, want unavailable", err)
	}
}

// TestSecretServicePropertiesGetCallFailure covers List()'s
// Properties.Get call failure, distinct from the corrupt-type branch.
func TestSecretServicePropertiesGetCallFailure(t *testing.T) {
	fs := newFakeSecretService()
	fs.failPropGet = true
	s := newTestSecretService(t, fs)
	if err := s.Set(context.Background(), "A", []byte("v")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if _, err := s.List(context.Background()); !isKind(err, cascade.KindUnavailable) {
		t.Fatalf("List = %v, want unavailable", err)
	}
}

// lockedItemPath is a fake item path used only to stand in for a locked
// search result; it never needs to resolve against anything.
const lockedItemPath dbus.ObjectPath = "/org/freedesktop/secrets/collection/login/locked1"
