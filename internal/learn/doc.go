// Package learn is Cascade Intelligence's per-job telemetry store
// (P1-E31-W6-S64-T1; 19-PEWS-PLAN-W6-W8-AGENTIC.md §Epic AE DECIDED,
// R-16.18, R-16.37 §Fleet capacity).
//
// Purpose: record a closed, allowlisted set of structured fields about
//
//	each terminal job outcome (never raw transcripts, prompt text, tool
//	output, or error strings) so a later learning pass can read
//	aggregate signal without ever touching anything a human or a
//	credential wrote.
//
// Inputs: TelemetryOutcome values built by the daemon's own job pipeline
//
//	(reconcile.go) from real jobs-domain rows; Finding values from
//	review/CI/adversarial/human-correction counters.
//
// Outputs: rows in the jobs-domain jobs_telemetry_outcomes and
//
//	jobs_telemetry_finding tables (own SetID "learn", R-16.77); an
//	outcome_class propagated onto the jobs-domain usage row (R-16.52);
//	a periodic retention sweep.
//
// Constraints (binding invariants, each independently tested):
//
//   - DOMAIN HOME (R-21.162): the telemetry tables live in the EXISTING
//     jobs domain (table prefix jobs_). No new storage domain is created;
//     internal/storage/domains.go is not touched. This package owns only
//     the migration set "learn" (SchemaVersion 1) inside that domain.
//   - NO PROMPT TEXT (R-21.152): TelemetryOutcome has no exported field
//     whose reflect.Kind is string or []byte and whose name matches
//     Prompt, Text, Content, Message, Input, Query, or Response —
//     TestNoPromptTextInvariant (outcome_test.go) asserts this by
//     reflection, not by convention. A planted credential canary in a
//     Record() argument must never reach a table, a journal, an event,
//     or a log — TestCredentialCanary (leak_test.go) fails the run
//     closed if it does.
//   - ALLOWLIST-ONLY (R-21.152): only the fields named in allowlist.go
//     are ever persisted. A field absent from the allowlist is a test
//     failure (TestAllowlistMatchesStruct, allowlist_test.go), not a
//     silent write.
//   - STRUCTURED FINDINGS (R-21.167): tool failures, review findings,
//     adversarial findings, and human corrections are rows of
//     jobs_telemetry_finding — {family, category, severity, count} over
//     closed enums (finding.go) — never free text, never hashed and
//     kept.
//   - OPAQUE IDENTIFIERS (R-21.162): repo_id, node_id, and every other
//     identifying column hold an opaque stable id or a bounded neutral
//     label — never an email, username, hostname, remote URL, or
//     absolute path (TestNoIdentifierLeakInvariant, outcome_test.go).
//
// SPORT: package:internal/learn — new (P1-E31-W6-S64-T1).
package learn
