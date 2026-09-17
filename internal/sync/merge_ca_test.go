package sync

// Purpose (this file): the content-addressed union, and the admission it
//   refuses to skip.
// SPORT: internal/sync tests (ADD) — P1-E17-W4-S38-T2.

import (
	"errors"
	"strings"
	"testing"
)

// admitted builds an admitted blob reference.
func admitted(addr string) BlobRef { return BlobRef{Address: addr, Admitted: true} }

// TestTheUnionLosesNothing is the blob domain's advantage over every other
// strategy: presence or absence, never a choice, so no side's data is
// discarded.
func TestTheUnionLosesNothing(t *testing.T) {
	a := map[string]BlobRef{"aa": admitted("aa"), "shared": admitted("shared")}
	b := map[string]BlobRef{"bb": admitted("bb"), "shared": admitted("shared")}

	ab, err := MergeContentAddressed(a, b)
	if err != nil {
		t.Fatalf("MergeContentAddressed: %v", err)
	}
	ba, err := MergeContentAddressed(b, a)
	if err != nil {
		t.Fatalf("MergeContentAddressed reversed: %v", err)
	}
	for _, addr := range []string{"aa", "bb", "shared"} {
		if _, ok := ab[addr]; !ok {
			t.Errorf("%q was lost in the union", addr)
		}
	}
	if len(ab) != len(ba) {
		t.Errorf("the union is not commutative: %d vs %d", len(ab), len(ba))
	}
}

// TestTheUnionIsIdempotent states merge(A, A) == A.
func TestTheUnionIsIdempotent(t *testing.T) {
	a := map[string]BlobRef{"aa": admitted("aa")}
	got, err := MergeContentAddressed(a, a)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Errorf("merge(A, A) has %d entries, A has 1", len(got))
	}
}

// TestAnUnadmittedBlobIsRefusedNotSkipped is the rule that keeps a
// corrupted transfer from looking like a blob that was never there.
func TestAnUnadmittedBlobIsRefusedNotSkipped(t *testing.T) {
	for _, tc := range []struct {
		name string
		a, b map[string]BlobRef
	}{
		{"unadmitted on the left", map[string]BlobRef{"bad": {Address: "bad"}}, nil},
		{"unadmitted on the right", nil, map[string]BlobRef{"bad": {Address: "bad"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := MergeContentAddressed(tc.a, tc.b)
			if err == nil {
				t.Fatal("an unadmitted blob entered the union")
			}
			if !errors.Is(err, ErrBlobNotAdmitted) {
				t.Errorf("err = %v, want it to wrap ErrBlobNotAdmitted", err)
			}
			if !strings.Contains(err.Error(), "bad") {
				t.Errorf("err = %v, want it to name the address", err)
			}
			if got != nil {
				t.Errorf("a refused union returned %v; a caller could use it", got)
			}
		})
	}
}

// TestTwoEncodingsOfOnePayloadBothSurvive states the deliberate cost. They
// have different digests, so they are different objects; collapsing them
// would require deciding which encoding is canonical, and a
// content-addressed store that rewrites addresses is not one.
func TestTwoEncodingsOfOnePayloadBothSurvive(t *testing.T) {
	got, err := MergeContentAddressed(
		map[string]BlobRef{"digest-of-chunking-a": admitted("digest-of-chunking-a")},
		map[string]BlobRef{"digest-of-chunking-b": admitted("digest-of-chunking-b")},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("%d blobs survived, want both encodings", len(got))
	}
}
