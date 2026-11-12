package jobs

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// TestRecordAcceptanceStoresConsumedFact checks delivered nonce immutability.
func TestRecordAcceptanceStoresConsumedFact(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s, _, _ := newNonceStore(t)
	ctx := context.Background()
	f := AcceptanceFact{Nonce: "delivered", CheckpointID: "cp", RepoID: "repo", JobSpecHash: "spec", PolicyVersion: "policy",
		Commit: "commit", Tree: "tree", PlanKinds: []string{"unit", "lint"}, AcceptedAt: attestStart}
	if err := s.RecordAcceptance(ctx, f); err != nil {
		t.Fatal(err)
	}
	f.PlanKinds = []string{"lint", "unit"}
	got, ok, err := s.AcceptedFor(ctx, f.Nonce, f.Commit, f.Tree, f.CheckpointID)
	if err != nil || !ok || !factEqual(got, f) {
		t.Fatalf("accepted: %+v %v %v", got, ok, err)
	}
	issued, ok, err := s.Issued(ctx, f.Nonce)
	if err != nil || !ok || issued.Commit != "" {
		t.Fatalf("issued: %+v %v %v", issued, ok, err)
	}
	if err := s.RecordAcceptance(ctx, f); err != nil {
		t.Fatal(err)
	}
	other := f
	other.RepoID = "other"
	if k, _ := cascade.KindOf(s.RecordAcceptance(ctx, other)); k != cascade.KindConflict {
		t.Fatal("conflict not refused")
	}
	got, _, _ = s.AcceptedFor(ctx, f.Nonce, f.Commit, f.Tree, f.CheckpointID)
	if !factEqual(got, f) {
		t.Fatal("conflict changed fact")
	}
	assertBadDeliveredFacts(t, s, f)
}

func assertBadDeliveredFacts(t *testing.T, s *AttestationNonceStore, f AcceptanceFact) {
	t.Helper()
	ctx := context.Background()
	bad := []AcceptanceFact{}
	for i := range 7 {
		b := f
		fields := []*string{&b.Nonce, &b.CheckpointID, &b.RepoID, &b.JobSpecHash, &b.PolicyVersion, &b.Commit, &b.Tree}
		*fields[i] = ""
		bad = append(bad, b)
	}
	for _, kinds := range [][]string{nil, {""}, {"a:b"}} {
		b := f
		b.PlanKinds = kinds
		bad = append(bad, b)
	}
	b := f
	b.AcceptedAt = time.Time{}
	bad = append(bad, b)
	for _, b := range bad {
		if k, _ := cascade.KindOf(s.RecordAcceptance(ctx, b)); k != cascade.KindInvalidInput {
			t.Fatalf("bad fact accepted: %+v", b)
		}
	}
}

// TestCiAttestationByNonce checks exact nonce selection.
func TestCiAttestationByNonce(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s, _ := NewCiAttestationStore(newTestStore(t).db)
	ctx := context.Background()
	for _, n := range []string{"n1", "n2"} {
		row := attestationRowFor(t, sampleAttestationRecord(n, n, strings.Repeat("b2", 20)))
		if err := s.Insert(ctx, row); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := s.ByNonce(ctx, "n1")
	if err != nil || len(rows) != 1 || rows[0].Nonce != "n1" {
		t.Fatalf("ByNonce: %+v %v", rows, err)
	}
	rows, err = s.ByNonce(ctx, "unknown")
	if err != nil || len(rows) != 0 {
		t.Fatalf("unknown: %+v %v", rows, err)
	}
}
