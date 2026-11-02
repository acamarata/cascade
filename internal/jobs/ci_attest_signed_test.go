package jobs

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestCiAttestSignedStoreWriteOnce(t *testing.T) {
	ctx := context.Background()
	st, err := NewCiAttestSignedStore(newTestStore(t).db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewCiAttestSignedStore(nil); err == nil {
		t.Fatal("nil db accepted")
	}
	run := NodeRun{ExecutionID: "exec-1", RunID: -5, RepoID: 9, CommitSHA: "c", Checkout: "/repo", RecordedAt: 10}
	if err := st.RecordRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	got, ok, err := st.Run(ctx, "exec-1")
	if err != nil || !ok || got != run {
		t.Fatalf("run %+v %v %v", got, ok, err)
	}
	if _, ok, err := st.Run(ctx, "unknown"); err != nil || ok {
		t.Fatalf("unknown run: %v %v", ok, err)
	}
	other := run
	other.CommitSHA = "d"
	if kind, _ := cascade.KindOf(st.RecordRun(ctx, other)); kind != cascade.KindConflict {
		t.Fatalf("rebind kind %v", kind)
	}
	if kind, _ := cascade.KindOf(st.RecordRun(ctx, NodeRun{ExecutionID: "x"})); kind != cascade.KindInvalidInput {
		t.Fatalf("empty run kind %v", kind)
	}
	if err := st.MarkSigned(ctx, "exec-1", "n1", 11); err != nil {
		t.Fatal(err)
	}
	if kind, _ := cascade.KindOf(st.MarkSigned(ctx, "exec-1", "n2", 12)); kind != cascade.KindConflict {
		t.Fatalf("second sign kind %v", kind)
	}
}
