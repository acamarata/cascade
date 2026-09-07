# corpus provenance

**PLACEHOLDER - owner prereq NOT met.** This directory is empty of labeled
records as of this ticket's run (P1-E12-W3-S25-T1). 06-FORGE-SPEC.md §7
lists "sanitized labeled transcript corpus (L/S-25.T1, U/S-45.T1,
F/S-12.T5)" as owner prereq #5: a corpus the owner deposits here before
this ticket starts, with this README stating source harnesses, collection
date, and label methodology.

No such corpus was present when this ticket ran. Per the ticket's task 1
("if absent, create the directory and a placeholder README that causes
TestSessionStateAccuracy to skip cleanly rather than panic"), this file is
that placeholder: `corpus_test.go`'s `TestSessionStateAccuracy` (build tag
`integration`) treats a directory with no `*.json` record files as
"corpus not deposited" and SKIPS - it does not fail, and it does not run
against zero records and report a false accuracy figure.

This is filed as an Art.9 defect against the corpus owner_prereq: the
`internal/fleet/sessions.SessionState` vocabulary this ticket ships
(`unknown`/`active`/`idle`/`blocked`/`stalled`/`closed`, see
`types.go`'s header comment) is a CHOSEN interim set, not corpus-derived,
built from the observation channels already available plus the R/S-39.T1
seam requirement that `blocked` and `stalled` be present. It may need
relabeling once the real corpus lands.

## Expected record format (once deposited)

Each labeled record is one `*.json` file directly under this directory.
`corpus_test.go` documents and validates this shape; a record that fails
to parse fails the run closed (a malformed record is excluded, not
silently skipped) per this ticket's sanitization requirement. Required
fields:

```json
{
  "session_id": "example",
  "as_of_unix_ms": 1700000000000,
  "current_state": "active",
  "expected_state": "idle",
  "census_present": true,
  "last_prompt_at_ms": null,
  "last_tool_at_ms": 1699999880000,
  "instructions_loaded_at_ms": null,
  "tool_count": 3,
  "transcript_parseable": true,
  "sse_closed": false
}
```

`current_state` and `expected_state` must be one of this package's
`SessionState.String()` values. `census_present: false` means the
labeled scenario has no tracked process (session ended).

## Required README fields once a real corpus lands

Replace this placeholder with:

- **Source harnesses**: which of claude/codex/opencode each record was
  collected from.
- **Collection date**: when the underlying sessions were observed.
- **Label methodology**: how `expected_state` was assigned (manual
  review, a prior heuristic, etc).
- **Sanitization**: confirmation that every record passed the A-T5
  identifier sweep and the planted-credential canary check (exact,
  base64, and hex form) before being committed - required because this
  repository is PUBLIC (R-21.152, mirroring L/S-24.T2's red-team clause).
  A record that does not pass this check must not be committed.
