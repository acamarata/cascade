package learn

// Purpose: the R-21.152 allowlist enforcement -- the exact, closed set of
//   TelemetryOutcome field names that may ever reach a table. A field
//   absent from this list is a test failure (allowlist_test.go's
//   TestAllowlistMatchesStruct), not a silent write: validateOutcomeAllowlist
//   also runs on every production Record() call, so a struct field added
//   without updating this file fails the very first write it makes, not
//   only CI.
// Inputs: a TelemetryOutcome value (reflected, never interpreted).
// Outputs: nil, or a KindInvalidInput error naming the offending field.
// Constraints: reflection walks EXPORTED fields only -- an unexported
//   field could never reach a caller-constructed TelemetryOutcome literal
//   in the first place, so there is nothing for this gate to police there.
// SPORT: internal.learn.validateOutcomeAllowlist/ADDED (P1-E31-W6-S64-T1).

import (
	"reflect"
	"sort"

	"github.com/acamarata/cascade/pkg/cascade"
)

// allowedOutcomeFields is the closed set (R-21.152) -- one entry per
// RECORDED FIELD the ticket names, in TelemetryOutcome's own Go field
// names.
var allowedOutcomeFields = map[string]bool{
	"JobID": true, "TaskClass": true, "RepoID": true, "Language": true,
	"Component": true, "RiskClass": true, "LaneTier": true, "NodeID": true,
	"ScopeRef": true, "ContextSizeTokens": true, "RetrievalStrategy": true,
	"DurationMS": true, "QueueTimeMS": true, "RetryCount": true,
	"CIFailureCount": true, "ReworkCycles": true, "FinalOutcome": true,
	"RollbackAt": true, "RegressionDetected": true, "CostTokens": true,
	"QuotaUnits": true,
}

// sortedOutcomeFields returns the allowlisted field names, sorted, so
// allowlist_test.go and roundtrip_test.go can assert TelemetryOutcome's actual
// field set never drifts from this list in either direction.
func sortedOutcomeFields() []string {
	names := make([]string, 0, len(allowedOutcomeFields))
	for name := range allowedOutcomeFields {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// validateOutcomeAllowlist refuses (KindInvalidInput) any exported field
// of o whose name is not in allowedOutcomeFields.
func validateOutcomeAllowlist(o TelemetryOutcome) error {
	t := reflect.TypeOf(o)
	for i := 0; i < t.NumField(); i++ {
		name := t.Field(i).Name
		if !allowedOutcomeFields[name] {
			return cascade.Newf(cascade.KindInvalidInput,
				"learn: field %q is not in the R-21.152 telemetry allowlist", name)
		}
	}
	return nil
}

// validateNoCredentialShapes refuses (KindInvalidInput) any exported
// string field of o that credentialShaped flags -- the fail-closed half of
// R-21.152's credential canary.
func validateNoCredentialShapes(o TelemetryOutcome) error {
	t := reflect.TypeOf(o)
	v := reflect.ValueOf(o)
	for i := 0; i < t.NumField(); i++ {
		if t.Field(i).Type.Kind() != reflect.String {
			continue
		}
		if credentialShaped(v.Field(i).String()) {
			return cascade.Newf(cascade.KindInvalidInput,
				"learn: field %q value matches a credential-shaped pattern; refused (R-21.152 credential canary)", t.Field(i).Name)
		}
	}
	return nil
}

// validateOutcome runs every gate Record calls before ever touching the
// db: allowlist membership, the credential canary over every string field
// (BEFORE any decoder, so a decoder's refusal can never be the path a
// secret leaves by), the identifying-column shape rules, then the two
// closed enums (fail-closed decode). No returned error carries a value.
func validateOutcome(o TelemetryOutcome) error {
	if err := validateOutcomeAllowlist(o); err != nil {
		return err
	}
	if err := validateNoCredentialShapes(o); err != nil {
		return err
	}
	if err := validateIdentifyingColumns(o); err != nil {
		return err
	}
	if _, err := DecodeLanguage(string(o.Language)); err != nil {
		return err
	}
	_, err := DecodeOutcomeClass(string(o.FinalOutcome))
	return err
}

// OutcomeClass is R-16.33's closed final_outcome enum. Unknown covers
// cancelled/failed (R-14.316); no fourth, permissive value exists.
type OutcomeClass string

const (
	// OutcomeAccepted marks a job whose terminal state was accepted.
	OutcomeAccepted OutcomeClass = "accepted"
	// OutcomeRejected marks a job whose terminal state was rejected.
	OutcomeRejected OutcomeClass = "rejected"
	// OutcomeUnknown marks a job whose terminal state was cancelled or failed.
	OutcomeUnknown OutcomeClass = "unknown"
)

// DecodeOutcomeClass fail-closed decodes raw: unrecognised is a typed error.
func DecodeOutcomeClass(raw string) (OutcomeClass, error) {
	switch OutcomeClass(raw) {
	case OutcomeAccepted, OutcomeRejected, OutcomeUnknown:
		return OutcomeClass(raw), nil
	default:
		return "", cascade.Newf(cascade.KindInvalidInput, "learn: final_outcome is not one of the closed enum values")
	}
}

// Language is the closed enum (DECISION, T0 -- no existing set names
// one): the languages Cascade's pipeline dispatches against, plus
// LanguageUnknown (reconcile.go's gap note).
type Language string

const (
	// LanguageGo marks a component written in Go.
	LanguageGo Language = "go"
	// LanguageTypeScript marks a component written in TypeScript.
	LanguageTypeScript Language = "typescript"
	// LanguageJavaScript marks a component written in JavaScript.
	LanguageJavaScript Language = "javascript"
	// LanguagePython marks a component written in Python.
	LanguagePython Language = "python"
	// LanguageRust marks a component written in Rust.
	LanguageRust Language = "rust"
	// LanguageOther marks a component written in a language outside this
	// closed set.
	LanguageOther Language = "other"
	// LanguageUnknown marks a job with no language signal available
	// (reconcile.go's gap note).
	LanguageUnknown Language = "unknown"
)

// DecodeLanguage fail-closed decodes raw.
func DecodeLanguage(raw string) (Language, error) {
	switch Language(raw) {
	case LanguageGo, LanguageTypeScript, LanguageJavaScript, LanguagePython, LanguageRust, LanguageOther, LanguageUnknown:
		return Language(raw), nil
	default:
		return "", cascade.Newf(cascade.KindInvalidInput, "learn: language is not one of the closed enum values")
	}
}
