// Purpose: every other path that could read or write a reserved daemon key
//
//	refuses it: the elevated and granted reads, the atomic (migration)
//	import before any write, the backup export (skipped) and backup import
//	(refused before any write), and the shared non-elevated read.
//
// SPORT: internal/secrets reserved-name path tests (P1-SEC-23).

package secrets

import (
	"bytes"
	"context"
	"crypto/rand"
	"testing"
	"time"
)

func TestReservedDaemonKeyRefusedByElevatedAndGrantedReads(t *testing.T) {
	ctx := context.Background()
	b, cc := newCountingBroker(t)
	b.gate = &allowGate{}
	if err := cc.Custody.Set(ctx, ApprovalKeyName, []byte("fake-daemon-key")); err != nil {
		t.Fatal(err)
	}
	_, err := b.Get(ctx, ApprovalKeyName)
	reservedRefusal(t, "vault get", err)
	grants, _, _ := newGrants(t)
	if _, err := grants.Issue(ctx, ApprovalKeyName, time.Hour); err != nil {
		t.Fatalf("issuing a grant: %v", err)
	}
	_, err = b.GetGranted(ctx, ApprovalKeyName, grants)
	reservedRefusal(t, "granted get", err)
	_, err = internalGet(ctx, b, ApprovalKeyName)
	reservedRefusal(t, "non-elevated read", err)
}

func TestImportAtomicRefusesReservedNameBeforeAnyWrite(t *testing.T) {
	ctx := context.Background()
	b, cc := newCountingBroker(t)
	_, err := planAtomicImportForTest(ctx, b, []EnvEntry{{Name: "AAA_FIRST", Value: []byte("fake")}, {Name: ApprovalKeyName, Value: []byte("fake")}})
	reservedRefusal(t, "atomic import", err)
	if cc.sets.Load() != 0 {
		t.Fatalf("the refused import wrote %d entries", cc.sets.Load())
	}
	// The apply step refuses on its own too, before its write.
	err = applyAtomicImport(ctx, b, []atomicImportEntry{{name: ApprovalKeyName, value: []byte("fake"), changed: true}})
	reservedRefusal(t, "atomic apply", err)
	if cc.sets.Load() != 0 {
		t.Fatalf("the refused apply wrote %d entries", cc.sets.Load())
	}
}

// planAtomicImportForTest runs ImportAtomic's planning and apply steps over
// parsed entries directly, so the apply-step guard is reached on its own
// too. (The vault.env parser accepts lower-case names, so a file can carry
// the reserved name; the migration test drives that path end to end.)
func planAtomicImportForTest(ctx context.Context, b *Broker, parsed []EnvEntry) (AtomicImportReport, error) {
	report, plan, err := planAtomicImport(ctx, b, parsed)
	if err != nil {
		return report, err
	}
	return report, applyAtomicImport(ctx, b, plan)
}

func TestBackupExportSkipsAndImportRefusesReservedKey(t *testing.T) {
	ctx := context.Background()
	src, scc := newCountingBroker(t)
	src.gate = &allowGate{}
	for name, v := range map[string]string{ApprovalKeyName: "fake-daemon-key", "AAA_TOKEN": "fake-a", "ZZZ_TOKEN": "fake-z"} {
		if err := scc.Custody.Set(ctx, name, []byte(v)); err != nil {
			t.Fatal(err)
		}
	}
	wrapped, err := src.Export(ctx, ExportRequest{Passphrase: "fake-backup-pass"})
	if err != nil {
		t.Fatal(err)
	}
	plain, err := ageDecrypt("fake-backup-pass", wrapped)
	if err != nil || bytes.Contains(plain, []byte(ApprovalKeyName)) || !bytes.Contains(plain, []byte("AAA_TOKEN")) {
		t.Fatalf("the export carries the daemon key or lost an entry (%v)", err)
	}
	dst, dcc := newCountingBroker(t)
	dst.gate = &allowGate{}
	if err := dst.Import(ctx, ImportRequest{Wrapped: wrapped, Passphrase: "fake-backup-pass"}); err != nil {
		t.Fatalf("round trip import: %v", err)
	}
	if got, _ := dcc.Custody.Get(ctx, "ZZZ_TOKEN"); string(got) != "fake-z" {
		t.Fatal("the round trip lost an entry")
	}
	forged := []byte(`{"version":1,"entries":[{"name":"AAA_NEW","value":"ZmFrZQ=="},{"name":"` + ApprovalKeyName + `","value":"ZmFrZQ=="}]}`)
	env, err := ageEncrypt("fake-backup-pass", forged, 10, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	before := dcc.sets.Load()
	reservedRefusal(t, "backup import", dst.Import(ctx, ImportRequest{Wrapped: env, Passphrase: "fake-backup-pass"}))
	if dcc.sets.Load() != before {
		t.Fatal("the refused backup import wrote an entry first")
	}
}

func TestPlainImportRefusesReservedNameBeforeAnyWrite(t *testing.T) {
	ctx := context.Background()
	b, cc := newCountingBroker(t)
	_, err := Import(ctx, b, []byte("AAA_FIRST=fake\n"+ApprovalKeyName+"=fake-replacement\n"))
	reservedRefusal(t, "vault import", err)
	if n := cc.sets.Load(); n != 0 {
		t.Fatalf("the refused vault import wrote %d entries first", n)
	}
}
