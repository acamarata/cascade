// Purpose: tests for AddProvider (add.go): the strict-insert primitive the
//   provider add/list split fix relies on to prove "duplicate refused,
//   never silently overwritten" as a property of the durable store itself,
//   independent of the CLI's own intentionally idempotent
//   `provider add` re-add-updates UX (cmd/cascade's own
//   TestProviderAddKeyIdempotentAcrossInvocations covers that layer).
// SPORT: provider.registry/ADD (P1-E10-W3-S20-T2 follow-up).

package registry

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestAddProviderThenGetProviderSameProcess(t *testing.T) {
	reg := newTestRegistry(t)
	ctx := context.Background()
	rec := sampleProvider("fresh")

	if err := reg.AddProvider(ctx, rec); err != nil {
		t.Fatalf("AddProvider: %v", err)
	}
	got, err := reg.GetProvider(ctx, "fresh")
	if err != nil {
		t.Fatalf("GetProvider: %v", err)
	}
	if got.Name != rec.Name || got.Driver != rec.Driver {
		t.Fatalf("roundtrip mismatch: got %+v", got)
	}
}

func TestAddProviderRefusesDuplicate(t *testing.T) {
	reg := newTestRegistry(t)
	ctx := context.Background()
	rec := sampleProvider("dup")

	if err := reg.AddProvider(ctx, rec); err != nil {
		t.Fatalf("first AddProvider: %v", err)
	}

	second := sampleProvider("dup")
	second.BaseURL = "https://different.example.com"
	err := reg.AddProvider(ctx, second)
	if err == nil {
		t.Fatal("expected a duplicate AddProvider to be refused")
	}
	if !cascade.HasKind(err, cascade.KindConflict) {
		t.Fatalf("expected KindConflict, got %v", err)
	}

	// The refusal must never silently overwrite: the original row's
	// BaseURL must still stand.
	got, err := reg.GetProvider(ctx, "dup")
	if err != nil {
		t.Fatalf("GetProvider after refused duplicate: %v", err)
	}
	if got.BaseURL != rec.BaseURL {
		t.Fatalf("duplicate AddProvider silently overwrote the record: BaseURL = %q, want %q", got.BaseURL, rec.BaseURL)
	}
}

func TestAddProviderInvalidRecordRefusedBeforeAnyWrite(t *testing.T) {
	reg := newTestRegistry(t)
	ctx := context.Background()
	bad := sampleProvider("bad")
	bad.Driver = DriverKind("not-a-real-driver")

	if err := reg.AddProvider(ctx, bad); err == nil {
		t.Fatal("expected an unknown driver_kind to be refused")
	} else if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("expected KindInvalidInput, got %v", err)
	}

	if _, err := reg.GetProvider(ctx, "bad"); !cascade.HasKind(err, cascade.KindNotFound) {
		t.Fatalf("a refused AddProvider must persist nothing, got %v", err)
	}
}

func TestAddProviderClosedHandleLeavesNothingPersisted(t *testing.T) {
	reg := newTestRegistry(t)
	ctx := context.Background()
	rec := sampleProvider("orphan")

	if err := reg.db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	err := reg.AddProvider(ctx, rec)
	if err == nil || !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("expected a KindUnavailable refusal on a closed handle, got %v", err)
	}
}
