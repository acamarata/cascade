// Purpose: the success leg of TestDurableWriteNeverBlanksEntry, split out
// to keep that test under the function-length limit: a verified write reads
// back by hash, the rollback slot holds the previous pair with a 24 h
// expiry, and the staging slot is gone.

package secrets

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"path/filepath"
	"testing"
	"time"
)

// hcCheckVerifiedWrite: a successful write reads back by hash and the
// rollback slot holds the old pair.
func hcCheckVerifiedWrite(t *testing.T) {
	t.Helper()
	dir := hcSetup(t)
	path := filepath.Join(dir, ".credentials.json")
	old, next := hcEntry("old"), hcEntry("new")
	hcWrite(t, path, old.Bytes(), 0o600)
	cust := &hcCustody{memCustody: newMemCustody()}
	h := hcCreds(hcFileStore(), cust)
	if err := h.WriteDurable(context.Background(), dir, next); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := h.Read(context.Background(), dir)
	if err != nil || sha256.Sum256(got.Bytes()) != sha256.Sum256(next.Bytes()) {
		t.Fatalf("read-back hash differs from the new pair (err %v)", err)
	}
	staged, rollback := slotNames(dir)
	slot := cust.entries[rollback]
	if len(slot) < 8 || !bytes.Equal(slot[8:], old.Bytes()) {
		t.Fatal("rollback slot does not hold the previous pair")
	}
	if exp := time.Unix(int64(binary.BigEndian.Uint64(slot[:8])), 0); !exp.Equal(hcEpoch.Add(RollbackWindow)) {
		t.Fatalf("rollback expiry %v, want 24 h after the clock", exp)
	}
	if _, ok := cust.entries[staged]; ok {
		t.Fatal("staging slot left behind after a verified write")
	}
}
