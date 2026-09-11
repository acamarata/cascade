// Purpose: Dedup's store-if-absent contract, including the second-run
//
//	dedup acceptance case (unchanged content stores zero new objects).
//
// SPORT: internal.backup.dedup/ADDED (P1-E19-W4-S41-T1).
package backup

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestDedupHashPayloadCountMismatch(t *testing.T) {
	_, err := Dedup(context.Background(), newMemTarget(), [][32]byte{{}}, nil)
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("Dedup(mismatched lengths) error kind = %v, want KindInvalidInput", err)
	}
}

func TestDedupStoresNewChunksOnly(t *testing.T) {
	ctx := context.Background()
	target := newMemTarget()
	hashes := [][32]byte{ObjectHash([]byte("a")), ObjectHash([]byte("b"))}
	payloads := [][]byte{[]byte("cipher-a"), []byte("cipher-b")}

	results, err := Dedup(ctx, target, hashes, payloads)
	if err != nil {
		t.Fatalf("Dedup: %v", err)
	}
	for i, r := range results {
		if !r.New {
			t.Fatalf("result[%d].New = false on first run, want true", i)
		}
	}
}

// TestDedupSecondRunStoresZeroNewObjects is the ticket's own acceptance
// case: running Dedup a second time over the identical hash/payload set
// must store zero new objects ("only new/changed chunks upload").
func TestDedupSecondRunStoresZeroNewObjects(t *testing.T) {
	ctx := context.Background()
	target := newMemTarget()
	hashes := [][32]byte{ObjectHash([]byte("x")), ObjectHash([]byte("y")), ObjectHash([]byte("z"))}
	payloads := [][]byte{[]byte("cx"), []byte("cy"), []byte("cz")}

	if _, err := Dedup(ctx, target, hashes, payloads); err != nil {
		t.Fatalf("Dedup (first run): %v", err)
	}
	results, err := Dedup(ctx, target, hashes, payloads)
	if err != nil {
		t.Fatalf("Dedup (second run): %v", err)
	}
	newCount := 0
	for _, r := range results {
		if r.New {
			newCount++
		}
	}
	if newCount != 0 {
		t.Fatalf("second Dedup run stored %d new objects, want 0", newCount)
	}
}

func TestDedupMixedNewAndExisting(t *testing.T) {
	ctx := context.Background()
	target := newMemTarget()
	existingHash := ObjectHash([]byte("already-there"))
	if _, err := PutObject(ctx, target, existingHash, []byte("stored")); err != nil {
		t.Fatalf("seed PutObject: %v", err)
	}
	newHash := ObjectHash([]byte("brand-new"))

	results, err := Dedup(ctx, target, [][32]byte{existingHash, newHash}, [][]byte{[]byte("ignored"), []byte("stored-new")})
	if err != nil {
		t.Fatalf("Dedup: %v", err)
	}
	if results[0].New {
		t.Fatal("result[0].New = true for a hash seeded before Dedup ran")
	}
	if !results[1].New {
		t.Fatal("result[1].New = false for a genuinely new hash")
	}
	got, err := GetObject(ctx, target, existingHash)
	if err != nil {
		t.Fatalf("GetObject: %v", err)
	}
	if string(got) != "stored" {
		t.Fatalf("Dedup overwrote an existing object: got %q, want %q", got, "stored")
	}
}
