package registry

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
)

func gatewayRecord() ProviderRecord {
	return ProviderRecord{Name: "gw1", Driver: DriverAnthropic, BaseURL: "https://gateway.invalid", Auth: AuthGateway,
		AuthRef: "gateway:acct", KnownModels: []string{"model-a"}, AccountKind: AccountPersonal, Tier: TierMid, HealthStatus: HealthUnknown}
}

// TestRegistryGatewayAuthRefShape checks the actual record validation boundary.
func TestRegistryGatewayAuthRefShape(t *testing.T) {
	for _, ref := range []VaultKeyRef{"gateway:acct", "provider.x.key", "gateway:", "gateway:../x", "gateway:UPPER", "gateway:-bad"} {
		rec := gatewayRecord()
		rec.AuthRef = ref
		err := rec.Validate()
		if ref == "gateway:acct" {
			if err != nil {
				t.Fatal(err)
			}
		} else if !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Fatalf("invalid ref accepted: %q", ref)
		}
	}
}

// TestRegistryRoundTripGatewayRecord uses a durable temporary providers.db.
func TestRegistryRoundTripGatewayRecord(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	db, err := sql.Open("sqlite", filepath.Join(home, "providers.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	ctx := context.Background()
	if err := ApplyMigrationSchema(ctx, db, migrate.SQLiteEmitter{}, newTestClock(), "", ""); err != nil {
		t.Fatal(err)
	}
	reg := NewRegistry(db, newTestClock())
	rec := gatewayRecord()
	if err := reg.UpsertProvider(ctx, rec); err != nil {
		t.Fatal(err)
	}
	got, err := reg.GetProvider(ctx, rec.Name)
	if err != nil {
		t.Fatal(err)
	}
	if got.Auth != AuthGateway || got.AuthRef != rec.AuthRef || got.BaseURL != rec.BaseURL || len(got.KnownModels) != 1 {
		t.Fatalf("roundtrip: %+v", got)
	}
}

// TestRegistryGatewayAuthRefNeverReadsVault refuses a tampered ref before resolution.
func TestRegistryGatewayAuthRefNeverReadsVault(t *testing.T) {
	reg := newTestRegistry(t)
	rec := gatewayRecord()
	rec.AuthRef = "provider.x.key"
	ctx := context.Background()
	if err := reg.UpsertProvider(ctx, rec); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("tampered gateway ref accepted on write: %v", err)
	}
	if _, err := reg.GetProvider(ctx, rec.Name); !cascade.HasKind(err, cascade.KindNotFound) {
		t.Fatal("invalid record persisted")
	}
	// A row tampered on disk after a valid write never leaves the registry,
	// so no consumer can hand its auth_ref to a vault resolver.
	if err := reg.UpsertProvider(ctx, gatewayRecord()); err != nil {
		t.Fatal(err)
	}
	if _, err := reg.db.ExecContext(ctx, `UPDATE `+tableProviderRecords+` SET auth_ref = ? WHERE name = ?`, "provider.x.key", rec.Name); err != nil {
		t.Fatal(err)
	}
	if got, err := reg.GetProvider(ctx, rec.Name); err == nil {
		t.Fatalf("tampered row returned by GetProvider: %+v", got)
	}
	if got, err := reg.ListProviders(ctx); err == nil {
		t.Fatalf("tampered row returned by ListProviders: %+v", got)
	}
}
