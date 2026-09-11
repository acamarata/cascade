package evidence

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func mustPutClaimWithEvidence(t *testing.T, s *Store, claimID string) Claim {
	t.Helper()
	c := Claim{
		ID: claimID, Statement: "s", Type: ClaimObservedFact, Confidence: 0.5,
		ProducedBy: ProducedBy{RunID: "run-1", LaneID: "lane-1"}, DataClass: DataClassInternal,
	}
	if err := s.PutClaim(context.Background(), c); err != nil {
		t.Fatalf("PutClaim: %v", err)
	}
	e := Evidence{
		ID: claimID + "-EVD", ClaimID: claimID,
		Source:      Source{Type: SourceFile, Path: "f.txt", Locator: Locator{Kind: LocatorBytes, Start: 0, End: 1}},
		ContentHash: "h", DataClass: DataClassInternal,
	}
	if err := s.PutEvidence(context.Background(), e); err != nil {
		t.Fatalf("PutEvidence: %v", err)
	}
	return c
}

func TestGetClaimWithoutEvidence(t *testing.T) {
	s, _ := newStore(t)
	c := Claim{
		ID: "CLM-1", Statement: "s", Type: ClaimObservedFact, Confidence: 0.5,
		ProducedBy: ProducedBy{RunID: "run-1"}, DataClass: DataClassInternal,
	}
	if err := s.PutClaim(context.Background(), c); err != nil {
		t.Fatalf("PutClaim: %v", err)
	}
	if _, err := s.GetClaim(context.Background(), "CLM-1"); err != ErrClaimWithoutEvidence {
		t.Fatalf("GetClaim before any evidence = %v, want ErrClaimWithoutEvidence", err)
	}
}

func TestGetClaimEstablishedAfterEvidence(t *testing.T) {
	s, _ := newStore(t)
	mustPutClaimWithEvidence(t, s, "CLM-2")
	got, err := s.GetClaim(context.Background(), "CLM-2")
	if err != nil {
		t.Fatalf("GetClaim: %v", err)
	}
	if got.ID != "CLM-2" {
		t.Errorf("GetClaim().ID = %q, want CLM-2", got.ID)
	}
}

func TestGetClaimNotFound(t *testing.T) {
	s, _ := newStore(t)
	if _, err := s.GetClaim(context.Background(), "CLM-MISSING"); err != ErrClaimNotFound {
		t.Fatalf("GetClaim(missing) = %v, want ErrClaimNotFound", err)
	}
}

func TestDataClassImmutable(t *testing.T) {
	s, _ := newStore(t)
	mustPutClaimWithEvidence(t, s, "CLM-3")
	updated := Claim{
		ID: "CLM-3", Statement: "s2", Type: ClaimObservedFact, Confidence: 0.6,
		ProducedBy: ProducedBy{RunID: "run-1"}, DataClass: DataClassSecret,
	}
	err := s.PutClaim(context.Background(), updated)
	if !cascade.HasKind(err, cascade.KindConflict) {
		t.Fatalf("PutClaim with changed data_class = %v, want typed conflict", err)
	}
}

func TestInvalidateStampsOnceThenConflicts(t *testing.T) {
	s, clock := newStore(t)
	mustPutClaimWithEvidence(t, s, "CLM-4")
	if err := s.Invalidate(context.Background(), "CLM-4"); err != nil {
		t.Fatalf("Invalidate: %v", err)
	}
	got, err := s.GetClaim(context.Background(), "CLM-4")
	if err != nil {
		t.Fatalf("GetClaim: %v", err)
	}
	if got.InvalidatedAt == nil || !got.InvalidatedAt.Equal(clock.Now()) {
		t.Errorf("InvalidatedAt = %v, want %v", got.InvalidatedAt, clock.Now())
	}
	if err := s.Invalidate(context.Background(), "CLM-4"); err != ErrClaimInvalidated {
		t.Fatalf("second Invalidate = %v, want ErrClaimInvalidated", err)
	}
}

func TestInvalidateUnknownClaim(t *testing.T) {
	s, _ := newStore(t)
	if err := s.Invalidate(context.Background(), "CLM-MISSING"); err != ErrClaimNotFound {
		t.Fatalf("Invalidate(missing) = %v, want ErrClaimNotFound", err)
	}
}

func TestAddContradictionAppends(t *testing.T) {
	s, _ := newStore(t)
	mustPutClaimWithEvidence(t, s, "CLM-5")
	if err := s.AddContradiction(context.Background(), "CLM-5", "CLM-OTHER"); err != nil {
		t.Fatalf("AddContradiction: %v", err)
	}
	got, err := s.GetClaim(context.Background(), "CLM-5")
	if err != nil {
		t.Fatalf("GetClaim: %v", err)
	}
	if len(got.Contradictions) != 1 || got.Contradictions[0] != "CLM-OTHER" {
		t.Errorf("Contradictions = %v, want [CLM-OTHER]", got.Contradictions)
	}
}

func TestAppendVerificationAppendsAndValidates(t *testing.T) {
	s, _ := newStore(t)
	mustPutClaimWithEvidence(t, s, "CLM-6")
	if err := s.AppendVerification(context.Background(), "CLM-6", Verification{RunID: "r2", Result: VerificationCorroborated}); err != nil {
		t.Fatalf("AppendVerification: %v", err)
	}
	got, err := s.GetClaim(context.Background(), "CLM-6")
	if err != nil {
		t.Fatalf("GetClaim: %v", err)
	}
	if len(got.Verification) != 1 || got.Verification[0].Result != VerificationCorroborated {
		t.Errorf("Verification = %v", got.Verification)
	}
	err = s.AppendVerification(context.Background(), "CLM-6", Verification{RunID: "r3", Result: "bogus"})
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("AppendVerification(bogus) = %v, want typed invalid-input", err)
	}
}

func TestClaimStoreMethodsOnClosedDB(t *testing.T) {
	s, _ := newStore(t)
	mustPutClaimWithEvidence(t, s, "CLM-CLOSE")
	if err := s.db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}
	ctx := context.Background()
	if err := s.PutClaim(ctx, validClaimFor("CLM-CLOSE2")); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Errorf("PutClaim on closed db = %v, want typed unavailable", err)
	}
	if _, err := s.ClaimsByRun(ctx, "run-1"); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Errorf("ClaimsByRun on closed db = %v, want typed unavailable", err)
	}
	if err := s.AddContradiction(ctx, "CLM-CLOSE", "CLM-OTHER"); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Errorf("AddContradiction on closed db = %v, want typed unavailable", err)
	}
	if err := s.AppendVerification(ctx, "CLM-CLOSE", Verification{RunID: "r", Result: VerificationUnverified}); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Errorf("AppendVerification on closed db = %v, want typed unavailable", err)
	}
}

func validClaimFor(id string) Claim {
	return Claim{
		ID: id, Statement: "s", Type: ClaimObservedFact, Confidence: 0.5,
		ProducedBy: ProducedBy{RunID: "run-1"}, DataClass: DataClassInternal,
	}
}

func TestClaimsByRunOrderedAndIncludesInvalidated(t *testing.T) {
	s, _ := newStore(t)
	mustPutClaimWithEvidence(t, s, "CLM-B")
	mustPutClaimWithEvidence(t, s, "CLM-A")
	if err := s.Invalidate(context.Background(), "CLM-A"); err != nil {
		t.Fatalf("Invalidate: %v", err)
	}
	claims, err := s.ClaimsByRun(context.Background(), "run-1")
	if err != nil {
		t.Fatalf("ClaimsByRun: %v", err)
	}
	if len(claims) != 2 || claims[0].ID != "CLM-A" || claims[1].ID != "CLM-B" {
		t.Fatalf("ClaimsByRun order = %+v, want [CLM-A, CLM-B]", claims)
	}
	if claims[0].InvalidatedAt == nil {
		t.Error("invalidated claim dropped InvalidatedAt from ClaimsByRun")
	}
}
