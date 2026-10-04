package learn

// Purpose: the identifying-column gate for TelemetryOutcome and Finding
//   inputs (R-21.162): identifying columns hold opaque ids and bounded
//   labels only, and no stored string longer than 64 characters is
//   anything but an enum value or an id. Every rule is checked before any
//   SQL runs, so a rejected value reaches neither table.
// Inputs: a TelemetryOutcome or a Finding (already past the allowlist).
// Outputs: nil, or a KindInvalidInput error that names the FIELD and the
//   RULE only. A rejected value never appears in an error: the value may
//   be the very secret or locating string the rule exists to keep out, and
//   an error is the thing callers log.
// Constraints: the credential scan (credential.go) always runs BEFORE any
//   decoder or shape rule, because those rules reject on shape and a
//   rejection path must not be the one that carries the value out. A JobID
//   the column cannot hold is mapped to an opaque digest id (storedJobID),
//   never refused, so one free-text job id cannot wedge the reconciler.
//   Empty values pass the shape rules (a neutral default stores nothing
//   identifying); required-ness is not this gate's job.
// SPORT: internal.learn.validateIdentifyingColumns/ADDED (P1-CAP-02).

import (
	"crypto/sha256"
	"encoding/hex"
	"reflect"
	"regexp"

	"github.com/acamarata/cascade/pkg/cascade"
)

// identifierMaxLen is the bound on every id and label column (R-21.162).
const identifierMaxLen = 64

var (
	// opaqueIDPattern is the shape of RepoID, NodeID and ScopeRef:
	// letters, digits, underscore and hyphen only. No dot, slash, colon,
	// at-sign or escape character, so a URL, path, e-mail address or host
	// name cannot match.
	opaqueIDPattern = regexp.MustCompile(`^([A-Za-z0-9][A-Za-z0-9_-]{0,63})?$`)
	// jobIDPattern is the shape of JobID: opaque-id characters plus dot and
	// colon, which jobs-domain ids may carry; still no slash, at-sign or space.
	jobIDPattern = regexp.MustCompile(`^([A-Za-z0-9][A-Za-z0-9_.:-]{0,63})?$`)
	// labelPattern is the shape of TaskClass, RiskClass, LaneTier,
	// RetrievalStrategy and Component: a bounded token with no whitespace,
	// dot, slash, colon or at-sign, so prose, host names, IP addresses and
	// locating strings cannot match. Every real label is a closed enum with
	// no dot; widen only with a named real label and a test.
	labelPattern = regexp.MustCompile(`^([A-Za-z0-9][A-Za-z0-9_+-]{0,63})?$`)
)

// Rule texts name the rule, never the value.
const (
	ruleOpaqueID = "an opaque id of at most 64 characters (letters, digits, underscore, hyphen)"
	ruleLabel    = "a bounded label of at most 64 characters (letters, digits, underscore, plus, hyphen)"
)

// identifyingRule pairs one TelemetryOutcome field with its shape rule.
type identifyingRule struct {
	field   string
	pattern *regexp.Regexp
	rule    string
}

// outcomeIdentifyingRules is the closed list of string columns this gate
// polices; Language and FinalOutcome are closed enums with their own
// decoders, and JobID is never refused on shape: storedJobID maps a job id
// the column cannot hold onto an opaque digest id instead.
var outcomeIdentifyingRules = []identifyingRule{
	{"RepoID", opaqueIDPattern, ruleOpaqueID},
	{"NodeID", opaqueIDPattern, ruleOpaqueID},
	{"TaskClass", labelPattern, ruleLabel},
	{"RiskClass", labelPattern, ruleLabel},
	{"LaneTier", labelPattern, ruleLabel},
	{"RetrievalStrategy", labelPattern, ruleLabel},
	{"Component", labelPattern, ruleLabel},
	// ScopeRef's only producer is scopeRefFor ("scope-" + hex digest), so it
	// takes the opaque-id rule: no percent, equals, plus or other escape
	// character a locating string could be smuggled through.
	{"ScopeRef", opaqueIDPattern, ruleOpaqueID},
}

// validateIdentifyingColumns refuses (KindInvalidInput) any identifying
// field of o that breaks its shape rule. The error names the field and the
// rule, never the rejected value.
func validateIdentifyingColumns(o TelemetryOutcome) error {
	v := reflect.ValueOf(o)
	for _, r := range outcomeIdentifyingRules {
		if !r.pattern.MatchString(v.FieldByName(r.field).String()) {
			return cascade.Newf(cascade.KindInvalidInput, "learn: field %q must be %s", r.field, r.rule)
		}
	}
	return nil
}

// validateFindingInputs runs the credential scan over a Finding's string
// inputs first, then refuses a negative Count. The family/category/severity
// enum decoders run after this and echo no value either; JobID is mapped
// by storedJobID, never refused on shape.
func validateFindingInputs(f Finding) error {
	for _, in := range []struct{ field, value string }{
		{"JobID", f.JobID}, {"Family", string(f.Family)},
		{"Category", string(f.Category)}, {"Severity", string(f.Severity)},
	} {
		if credentialShaped(in.value) {
			return cascade.Newf(cascade.KindInvalidInput,
				"learn: finding field %q value matches a credential-shaped pattern; refused", in.field)
		}
	}
	if f.Count < 0 {
		return cascade.Newf(cascade.KindInvalidInput, "learn: finding field %q must be zero or more", "Count")
	}
	return nil
}

// storedJobID is the job_id both writers store for raw: raw itself when it
// fits jobIDPattern, otherwise the opaque "job-" digest id, so a job id
// minted from free text (the planner's "intent:" ids) never wedges a
// writer and never reaches a table. Record and WriteFinding apply the same
// mapping, so a finding still finds its outcome; the jobs_usage join keeps
// the raw id. Callers run the credential scan on raw first.
func storedJobID(raw string) string {
	if jobIDPattern.MatchString(raw) {
		return raw
	}
	return "job-" + digestID(raw)
}

// scopeRefFor maps a job's mutable scope (repo-relative footprint globs,
// never a scope id) onto an opaque "scope-" digest id. An empty scope
// stays empty. The raw footprint never reaches the table.
func scopeRefFor(raw string) string {
	if raw == "" {
		return ""
	}
	return "scope-" + digestID(raw)
}

// labelFor maps a job's class string onto a label the writer will accept:
// a value within labelPattern that carries no credential passes through;
// anything else becomes the neutral "unknown" default the reconciler uses
// for every signal it cannot supply, so one bad job never wedges a pass.
func labelFor(raw string) string {
	if labelPattern.MatchString(raw) && !credentialShaped(raw) {
		return raw
	}
	return "unknown"
}

// digestID is the first 16 hex characters of the SHA-256 of raw.
func digestID(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:8])
}
