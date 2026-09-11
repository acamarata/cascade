package evidence

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func mustPutBareClaim(t *testing.T, s *Store, claimID string) {
	t.Helper()
	c := Claim{
		ID: claimID, Statement: "s", Type: ClaimObservedFact, Confidence: 0.5,
		ProducedBy: ProducedBy{RunID: "run-1"}, DataClass: DataClassInternal,
	}
	if err := s.PutClaim(context.Background(), c); err != nil {
		t.Fatalf("PutClaim: %v", err)
	}
}

func TestPutEvidenceUnknownClaim(t *testing.T) {
	s, _ := newStore(t)
	e := Evidence{
		ID: "EVD-1", ClaimID: "CLM-MISSING",
		Source: Source{Type: SourceFile, Path: "f", Locator: Locator{Kind: LocatorBytes, Start: 0, End: 1}}, ContentHash: "h", DataClass: DataClassInternal,
	}
	if err := s.PutEvidence(context.Background(), e); err != ErrClaimNotFound {
		t.Fatalf("PutEvidence(unknown claim) = %v, want ErrClaimNotFound", err)
	}
}

func TestEvidenceByIDNotFound(t *testing.T) {
	s, _ := newStore(t)
	if _, err := s.EvidenceByID(context.Background(), "EVD-MISSING"); err != ErrEvidenceNotFound {
		t.Fatalf("EvidenceByID(missing) = %v, want ErrEvidenceNotFound", err)
	}
}

func TestEvidenceDataClassImmutable(t *testing.T) {
	s, _ := newStore(t)
	mustPutBareClaim(t, s, "CLM-7")
	e := Evidence{
		ID: "EVD-7", ClaimID: "CLM-7",
		Source: Source{Type: SourceFile, Path: "f", Locator: Locator{Kind: LocatorBytes, Start: 0, End: 1}}, ContentHash: "h", DataClass: DataClassInternal,
	}
	if err := s.PutEvidence(context.Background(), e); err != nil {
		t.Fatalf("PutEvidence: %v", err)
	}
	e.DataClass = DataClassSecret
	err := s.PutEvidence(context.Background(), e)
	if !cascade.HasKind(err, cascade.KindConflict) {
		t.Fatalf("PutEvidence with changed data_class = %v, want typed conflict", err)
	}
}

func TestEvidenceForClaimOrderedByID(t *testing.T) {
	s, _ := newStore(t)
	mustPutBareClaim(t, s, "CLM-8")
	for _, id := range []string{"EVD-B", "EVD-A"} {
		e := Evidence{
			ID: id, ClaimID: "CLM-8",
			Source: Source{Type: SourceGit, Repository: "/r", Commit: "c", Path: "p",
				Locator: Locator{Kind: LocatorLines, Start: 1, End: 1}},
			ContentHash: "h", DataClass: DataClassInternal,
		}
		if err := s.PutEvidence(context.Background(), e); err != nil {
			t.Fatalf("PutEvidence(%s): %v", id, err)
		}
	}
	evs, err := s.EvidenceForClaim(context.Background(), "CLM-8")
	if err != nil {
		t.Fatalf("EvidenceForClaim: %v", err)
	}
	if len(evs) != 2 || evs[0].ID != "EVD-A" || evs[1].ID != "EVD-B" {
		t.Fatalf("EvidenceForClaim order = %+v, want [EVD-A, EVD-B]", evs)
	}
	if evs[0].Source.Locator.Kind != LocatorLines || evs[0].Source.Commit != "c" {
		t.Errorf("EvidenceForClaim source fields lost round trip: %+v", evs[0].Source)
	}
}

func TestEvidenceByIDSuccess(t *testing.T) {
	s, _ := newStore(t)
	mustPutBareClaim(t, s, "CLM-BYID")
	e := Evidence{
		ID: "EVD-BYID", ClaimID: "CLM-BYID",
		Source:      Source{Type: SourceFile, Path: "f", Locator: Locator{Kind: LocatorBytes, Start: 0, End: 1}},
		ContentHash: "h", DataClass: DataClassInternal,
	}
	if err := s.PutEvidence(context.Background(), e); err != nil {
		t.Fatalf("PutEvidence: %v", err)
	}
	got, err := s.EvidenceByID(context.Background(), "EVD-BYID")
	if err != nil {
		t.Fatalf("EvidenceByID: %v", err)
	}
	if got.ID != "EVD-BYID" || got.Source.Path != "f" {
		t.Errorf("EvidenceByID = %+v, want ID=EVD-BYID Path=f", got)
	}
}

func TestEvidenceStoreMethodsOnClosedDB(t *testing.T) {
	s, _ := newStore(t)
	mustPutBareClaim(t, s, "CLM-ECLOSE")
	if err := s.db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}
	ctx := context.Background()
	e := Evidence{
		ID: "EVD-ECLOSE", ClaimID: "CLM-ECLOSE",
		Source:      Source{Type: SourceFile, Path: "f", Locator: Locator{Kind: LocatorBytes, Start: 0, End: 1}},
		ContentHash: "h", DataClass: DataClassInternal,
	}
	if err := s.PutEvidence(ctx, e); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Errorf("PutEvidence on closed db = %v, want typed unavailable", err)
	}
	if _, err := s.EvidenceForClaim(ctx, "CLM-ECLOSE"); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Errorf("EvidenceForClaim on closed db = %v, want typed unavailable", err)
	}
}

func TestPutEvidenceRejectsInvalid(t *testing.T) {
	s, _ := newStore(t)
	mustPutBareClaim(t, s, "CLM-9")
	bad := Evidence{ID: "", ClaimID: "CLM-9"}
	if err := s.PutEvidence(context.Background(), bad); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("PutEvidence(empty id) = %v, want typed invalid-input", err)
	}
}
