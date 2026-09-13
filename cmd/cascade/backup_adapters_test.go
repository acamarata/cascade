// Purpose: unit coverage for backup.go's small composition-root adapters
//
//	(backupProofGate, backupVaultAdapter, backupVaultStoreAdapter,
//	decodeBackupPubKey), each of which shipped with zero direct callers
//	in the existing ceremony-level suite (they are only reached today
//	when a CLI path happens to exercise --with-vault/key-ceremony flags),
//	isolated here over a real *secrets.Broker and an in-memory
//	secrets.Custody double, so no vault ceremony flags or file-vault
//	fixtures are needed to prove each adapter method's wiring is real.
//
// SPORT: cmd.cascade.backup/TEST (P1-E19-W4-S42-T3/T4).
package main

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/internal/backup"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
)

// memCustody is a minimal in-memory secrets.Custody double for this
// file's adapter tests -- a real collaborator satisfying the exported
// interface, not a broker mock.
type memCustody struct{ entries map[string][]byte }

func newMemCustody() *memCustody { return &memCustody{entries: map[string][]byte{}} }

func (m *memCustody) Name() string    { return "memory" }
func (m *memCustody) Available() bool { return true }

func (m *memCustody) Set(_ context.Context, name string, value []byte) error {
	m.entries[name] = append([]byte(nil), value...)
	return nil
}

func (m *memCustody) Get(_ context.Context, name string) ([]byte, error) {
	v, ok := m.entries[name]
	if !ok {
		return nil, cascade.Newf(cascade.KindNotFound, "no secret named %q", name)
	}
	return v, nil
}

func (m *memCustody) Delete(_ context.Context, name string) error {
	delete(m.entries, name)
	return nil
}

func (m *memCustody) List(_ context.Context) ([]string, error) {
	names := make([]string, 0, len(m.entries))
	for name := range m.entries {
		names = append(names, name)
	}
	return names, nil
}

func TestDecodeBackupPubKey(t *testing.T) {
	if _, err := decodeBackupPubKey("not-base64!!"); err == nil {
		t.Error("decodeBackupPubKey(invalid base64) = nil error, want a refusal")
	}
	if _, err := decodeBackupPubKey(""); err == nil {
		t.Error("decodeBackupPubKey(empty) = nil error, want a refusal (wrong key size)")
	}
}

// TestBackupProofGate_Authorize proves the exact verb table this file's
// own doc comment specifies: an empty proof always refuses, and a minted
// proof authorizes only VerbBackupExport/VerbBackupImport/VerbGet, never
// an arbitrary verb -- fail-closed for anything not on that list.
func TestBackupProofGate_Authorize(t *testing.T) {
	empty := backupProofGate{}
	if err := empty.Authorize(context.Background(), secrets.VerbGet); err == nil {
		t.Error("empty proof authorized VerbGet, want ErrElevationRequired")
	}

	minted := backupProofGate{proof: backup.ElevationProof("proof-1")}
	for _, verb := range []string{secrets.VerbBackupExport, secrets.VerbBackupImport, secrets.VerbGet} {
		if err := minted.Authorize(context.Background(), verb); err != nil {
			t.Errorf("minted proof refused verb %q: %v", verb, err)
		}
	}
	if err := minted.Authorize(context.Background(), "some.other.verb"); err == nil {
		t.Error("minted proof authorized an unlisted verb, want ErrElevationRequired")
	}
}

// TestBackupVaultAdapter_ExportImportRoundTrip drives Export then Import
// through a real *secrets.Broker (over an in-memory custody), proving
// both adapter methods actually forward to the broker's own Export/
// Import rather than being unreachable dead wiring.
func TestBackupVaultAdapter_ExportImportRoundTrip(t *testing.T) {
	custody := newMemCustody()
	custody.entries["TOKEN"] = []byte("secret-value")
	broker, err := secrets.NewBroker(custody, backupProofGate{proof: backup.ElevationProof("p")})
	if err != nil {
		t.Fatalf("NewBroker: %v", err)
	}
	adapter := backupVaultAdapter{broker: broker}

	wrapped, err := adapter.Export(context.Background(), backup.VaultExportRequest{Passphrase: "hunter2"})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if len(wrapped) == 0 {
		t.Fatal("Export returned empty bytes")
	}

	custody2 := newMemCustody()
	broker2, err := secrets.NewBroker(custody2, backupProofGate{proof: backup.ElevationProof("p")})
	if err != nil {
		t.Fatalf("NewBroker: %v", err)
	}
	adapter2 := backupVaultAdapter{broker: broker2}
	if err := adapter2.Import(context.Background(), backup.VaultImportRequest{Wrapped: wrapped, Passphrase: "hunter2"}); err != nil {
		t.Fatalf("Import: %v", err)
	}
	if got := string(custody2.entries["TOKEN"]); got != "secret-value" {
		t.Errorf("after Import, TOKEN = %q, want \"secret-value\"", got)
	}
}

// TestBackupVaultStoreAdapter_ExistsGetSet drives all three narrow
// VaultStore methods through a real *secrets.Broker.
func TestBackupVaultStoreAdapter_ExistsGetSet(t *testing.T) {
	custody := newMemCustody()
	broker, err := secrets.NewBroker(custody, backupProofGate{proof: backup.ElevationProof("p")})
	if err != nil {
		t.Fatalf("NewBroker: %v", err)
	}
	store := backupVaultStoreAdapter{broker: broker}
	ctx := context.Background()

	if ok, err := store.Exists(ctx, "AGE_IDENTITY"); err != nil || ok {
		t.Fatalf("Exists before Set = (%v, %v), want (false, nil)", ok, err)
	}
	if err := store.Set(ctx, "AGE_IDENTITY", []byte("identity-bytes")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if ok, err := store.Exists(ctx, "AGE_IDENTITY"); err != nil || !ok {
		t.Fatalf("Exists after Set = (%v, %v), want (true, nil)", ok, err)
	}
	got, err := store.Get(ctx, "AGE_IDENTITY")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != "identity-bytes" {
		t.Errorf("Get = %q, want \"identity-bytes\"", got)
	}
}
