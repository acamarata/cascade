package learn

// Purpose: R-21.167's structured findings -- the four count fields that
//   used to summarise free-text material (tool failures, review findings,
//   adversarial findings, human corrections) become jobs_telemetry_finding
//   rows: {family, category, severity, count} over closed enums, fail-
//   closed decode, no free-text parameter anywhere in this file.
// Inputs: a Finding value naming one outcome's job_id plus the closed
//   {family, category, severity, count} tuple.
// Outputs: one jobs_telemetry_finding row, FK'd to its outcome.
// Constraints: category is validated against its OWN family's closed set
//   -- "unknown" is never a permissive zero value; an invalid pair is a
//   typed error, never silently coerced or dropped.
// SPORT: internal.learn.FindingWriter/ADDED (P1-E31-W6-S64-T1).

import (
	"context"
	"database/sql"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Family is R-21.167's closed top-level finding classification.
type Family string

const (
	// FamilyToolFailure covers a tool invocation that failed.
	FamilyToolFailure Family = "tool_failure"
	// FamilyReview covers a code-review finding.
	FamilyReview Family = "review"
	// FamilyAdversarial covers an adversarial-review finding.
	FamilyAdversarial Family = "adversarial"
	// FamilyHumanCorrection covers a human-applied correction.
	FamilyHumanCorrection Family = "human_correction"
)

// Category is a family's own closed sub-classification. DECISION (T0 --
// no existing enumeration names these): a small, representative set per
// family, closed by validCategories below.
type Category string

const (
	// CategoryTimeout is a tool_failure category -- the tool timed out.
	CategoryTimeout Category = "timeout"
	// CategoryCrash is a tool_failure category -- the tool crashed.
	CategoryCrash Category = "crash"
	// CategoryInvalidOutput is a tool_failure category -- malformed output.
	CategoryInvalidOutput Category = "invalid_output"
	// CategoryPermissionDenied is a tool_failure category -- access refused.
	CategoryPermissionDenied Category = "permission_denied"
	// CategoryStyle is a review category -- a style finding.
	CategoryStyle Category = "style"
	// CategoryCorrectness is a review category -- a correctness bug.
	CategoryCorrectness Category = "correctness"
	// CategorySecurity is a review category -- a security finding.
	CategorySecurity Category = "security"
	// CategoryMissingTest is a review category -- missing test coverage.
	CategoryMissingTest Category = "missing_test"
	// CategoryPromptInjection is an adversarial category.
	CategoryPromptInjection Category = "prompt_injection"
	// CategoryPolicyBypass is an adversarial category.
	CategoryPolicyBypass Category = "policy_bypass"
	// CategoryDataExfiltration is an adversarial category.
	CategoryDataExfiltration Category = "data_exfiltration"
	// CategoryPrivilegeEscalation is an adversarial category.
	CategoryPrivilegeEscalation Category = "privilege_escalation"
	// CategoryFactual is a human_correction category -- a factual fix.
	CategoryFactual Category = "factual"
	// CategoryScope is a human_correction category -- a scope fix.
	CategoryScope Category = "scope"
	// CategoryStylePreference is a human_correction category.
	CategoryStylePreference Category = "style_preference"
	// CategorySafety is a human_correction category -- a safety fix.
	CategorySafety Category = "safety"
)

// validCategories closes each family's category set.
var validCategories = map[Family]map[Category]bool{
	FamilyToolFailure: {CategoryTimeout: true, CategoryCrash: true, CategoryInvalidOutput: true, CategoryPermissionDenied: true},
	FamilyReview:      {CategoryStyle: true, CategoryCorrectness: true, CategorySecurity: true, CategoryMissingTest: true},
	FamilyAdversarial: {CategoryPromptInjection: true, CategoryPolicyBypass: true, CategoryDataExfiltration: true, CategoryPrivilegeEscalation: true},
	FamilyHumanCorrection: {
		CategoryFactual: true, CategoryScope: true, CategoryStylePreference: true, CategorySafety: true,
	},
}

// Severity is R-21.167's closed severity enum.
type Severity string

const (
	// SeverityLow is the lowest severity.
	SeverityLow Severity = "low"
	// SeverityNormal is the default severity.
	SeverityNormal Severity = "normal"
	// SeverityHigh is an elevated severity.
	SeverityHigh Severity = "high"
	// SeverityCritical is the highest severity.
	SeverityCritical Severity = "critical"
)

var validSeverities = map[Severity]bool{SeverityLow: true, SeverityNormal: true, SeverityHigh: true, SeverityCritical: true}

// DecodeFamilyCategory fail-closed validates family/category together:
// an unknown family, an unknown category, or a category outside its
// family's own closed set is a typed error.
func DecodeFamilyCategory(family, category string) (Family, Category, error) {
	f := Family(family)
	set, ok := validCategories[f]
	if !ok {
		return "", "", cascade.New(cascade.KindInvalidInput, "learn: finding family is not one of the closed enum values")
	}
	c := Category(category)
	if !set[c] {
		return "", "", cascade.New(cascade.KindInvalidInput, "learn: finding category is not valid for the finding family")
	}
	return f, c, nil
}

// DecodeSeverity fail-closed validates severity.
func DecodeSeverity(severity string) (Severity, error) {
	s := Severity(severity)
	if !validSeverities[s] {
		return "", cascade.New(cascade.KindInvalidInput, "learn: finding severity is not one of the closed enum values")
	}
	return s, nil
}

// Finding is one structured {family, category, severity, count} row, keyed
// to the outcome for JobID. No free-text parameter exists anywhere on this
// type (R-21.167).
type Finding struct {
	JobID    string
	Family   Family
	Category Category
	Severity Severity
	Count    int
}

// FindingWriter is the seam a caller writes findings through.
// SQLiteFindingWriter satisfies it in production.
type FindingWriter interface {
	WriteFinding(ctx context.Context, f Finding) error
}

// SQLiteFindingWriter is FindingWriter's real modernc/sqlite implementation.
type SQLiteFindingWriter struct{ db *sql.DB }

// NewSQLiteFindingWriter returns a writer persisting through db.
func NewSQLiteFindingWriter(db *sql.DB) *SQLiteFindingWriter { return &SQLiteFindingWriter{db: db} }

var _ FindingWriter = (*SQLiteFindingWriter)(nil)

// WriteFinding resolves f.JobID's outcome_id (through storedJobID, the
// same mapping Record stores) and inserts one jobs_telemetry_finding row.
// Refuses (KindNotFound) when no outcome row exists yet for f.JobID -- a
// finding can never attach to nothing -- and (KindInvalidInput) a negative
// Count.
func (w *SQLiteFindingWriter) WriteFinding(ctx context.Context, f Finding) error {
	if w == nil || w.db == nil {
		return cascade.New(cascade.KindInvalidInput, "learn: WriteFinding requires a constructed SQLiteFindingWriter")
	}
	if err := validateFindingInputs(f); err != nil {
		return err
	}
	if _, _, err := DecodeFamilyCategory(string(f.Family), string(f.Category)); err != nil {
		return err
	}
	if _, err := DecodeSeverity(string(f.Severity)); err != nil {
		return err
	}
	var outcomeID int64
	err := w.db.QueryRowContext(ctx, `SELECT id FROM `+tableTelemetryOutcomes+` WHERE job_id = ?`, storedJobID(f.JobID)).Scan(&outcomeID)
	if err == sql.ErrNoRows {
		return cascade.New(cascade.KindNotFound, "learn: no telemetry outcome recorded yet for the finding's job_id")
	}
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "learn: resolve outcome_id for finding")
	}
	_, err = w.db.ExecContext(ctx,
		`INSERT INTO `+tableTelemetryFinding+` (outcome_id, family, category, severity, count) VALUES (?,?,?,?,?)`,
		outcomeID, string(f.Family), string(f.Category), string(f.Severity), f.Count)
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "learn: insert jobs_telemetry_finding row")
	}
	return nil
}
