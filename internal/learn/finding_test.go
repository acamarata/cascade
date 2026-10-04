// Purpose: FindingWriter tests -- structured round trip, closed-enum
//
//	refusal (fail-closed decode), and the no-outcome-yet refusal.
//
// SPORT: learn/finding/ADD (P1-E31-W6-S64-T1).
package learn

import (
	"context"
	"testing"
)

// TestTelemetryFindingStructured: a valid Finding round-trips as a
// structured row keyed to its outcome; an unknown category for a family,
// and an unknown severity, are both refused rather than coerced.
func TestTelemetryFindingStructured(t *testing.T) {
	db := newTestOutcomeDB(t)
	ctx := context.Background()
	ow := NewSQLiteOutcomeWriter(db, newTestClock())
	if err := ow.Record(ctx, baseOutcome("job-finding-1")); err != nil {
		t.Fatalf("seed outcome: %v", err)
	}
	fw := NewSQLiteFindingWriter(db)
	f := Finding{JobID: "job-finding-1", Family: FamilyReview, Category: CategoryCorrectness, Severity: SeverityHigh, Count: 3}
	if err := fw.WriteFinding(ctx, f); err != nil {
		t.Fatalf("WriteFinding: %v", err)
	}
	var family, category, severity string
	var count int
	row := db.QueryRowContext(ctx, `SELECT family, category, severity, count FROM `+tableTelemetryFinding+` WHERE outcome_id =
		(SELECT id FROM `+tableTelemetryOutcomes+` WHERE job_id = ?)`, "job-finding-1")
	if err := row.Scan(&family, &category, &severity, &count); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if family != string(FamilyReview) || category != string(CategoryCorrectness) || severity != string(SeverityHigh) || count != 3 {
		t.Errorf("got (%q,%q,%q,%d), want (review,correctness,high,3)", family, category, severity, count)
	}

	// Category outside its family's closed set: refused, not coerced.
	bad := Finding{JobID: "job-finding-1", Family: FamilyReview, Category: CategoryPromptInjection, Severity: SeverityLow, Count: 1}
	if err := fw.WriteFinding(ctx, bad); err == nil {
		t.Error("WriteFinding with a cross-family category = nil error, want refusal")
	}

	// Unknown severity: refused.
	badSev := Finding{JobID: "job-finding-1", Family: FamilyReview, Category: CategoryStyle, Severity: "catastrophic", Count: 1}
	if err := fw.WriteFinding(ctx, badSev); err == nil {
		t.Error("WriteFinding with an unknown severity = nil error, want refusal")
	}

	// No outcome yet for this job_id: refused (KindNotFound).
	orphan := Finding{JobID: "job-no-outcome", Family: FamilyToolFailure, Category: CategoryTimeout, Severity: SeverityNormal, Count: 1}
	if err := fw.WriteFinding(ctx, orphan); err == nil {
		t.Error("WriteFinding for a job with no outcome row = nil error, want refusal")
	}
}

// TestDecodeFamilyCategory covers the fail-closed decode paths directly.
func TestDecodeFamilyCategory(t *testing.T) {
	if _, _, err := DecodeFamilyCategory("tool_failure", "crash"); err != nil {
		t.Errorf("valid pair refused: %v", err)
	}
	if _, _, err := DecodeFamilyCategory("not_a_family", "crash"); err == nil {
		t.Error("unknown family accepted, want refusal")
	}
	if _, _, err := DecodeFamilyCategory("tool_failure", "style"); err == nil {
		t.Error("cross-family category accepted, want refusal")
	}
}

func TestDecodeSeverity(t *testing.T) {
	if _, err := DecodeSeverity("high"); err != nil {
		t.Errorf("valid severity refused: %v", err)
	}
	if _, err := DecodeSeverity("extreme"); err == nil {
		t.Error("unknown severity accepted, want refusal")
	}
}
