# ADR-E09T6: Harness transcript format stability

- Status: Accepted (spike finding)
- Ticket: P1-E05-W2-S09-T6 (Art.12 risk spike, no dep on and no scope claim
  over the ticket it de-risks)
- Consumed by: L/S-24.T2 (fleet/harness-watch transcript tailer) at that
  ticket's own scheduling
- Evidence: `internal/context/testdata/transcripts/` (README has full
  provenance per file)

## The question

Before Epic L builds a transcript parser for the fleet/harness-watch
subsystem, this spike answers: (1) which transcript fields are stable vs.
volatile across harness versions, per harness; (2) whether each format
carries an explicit version signal a parser can dispatch on; (3) whether one
versioned-parser design covers all three harnesses, or per-harness fallback
strategies are required; (4) what the graceful-degrade path must be when an
unrecognised format version is seen.

## Answer, stated plainly

**No single versioned-parser design covers all three harnesses as one
shape, and the reason is not incidental.** Two of the three (CC, codex) are
line-oriented JSON files with an explicit, per-session version string, and
a single `TranscriptParser` interface with per-harness implementations
covers them cleanly. The third (opencode) **does not write a transcript
file to disk at all** -- its session state lives in a SQLite database, and
one of its two observed version values is the literal string `local`
rather than anything comparable as a version. This is a real,
evidence-backed negative finding for opencode specifically: a file-tailer
design, which is what "transcript tailer" in the subsystem's own name
implies, cannot ingest opencode's session state without an entirely
different read path (a DB reader, not a file watcher), and that read path
needs a defined behaviour for the `local` sentinel before it can dispatch
on version at all. CC and codex support the file-tailer model; opencode
does not. L/S-24.T2's scope should be amended to say so rather than
discovering it mid-implementation.

The recommendation below is still a single Go interface (`TranscriptParser`
with per-harness implementations, one of which reads a database instead of
a file), because the abstraction the parser dispatch needs to make -- "given
raw input claiming to be harness X, either return typed events or fail
closed" -- is identical across all three. What is not identical, and must
not be pretended to be identical, is how each implementation gets its raw
input.

## Per-harness field stability table

### CC

| Field | Stability | Notes |
|---|---|---|
| `version` | present, stable field name | Stamped on every non-`queue-operation`/`last-prompt` line. Real values observed on this machine: `2.1.140` through `2.1.261` across 11+ distinct point releases. This is the harness's own release version, not a dedicated transcript-schema version -- see "version-signal identification" below. |
| `type` | stable | Enum observed: `queue-operation`, `attachment`, `user`, `assistant`, `system`, `last-prompt`. All six seen in real local session history. |
| `sessionId`, `timestamp` | stable | Present on effectively every line type. |
| `uuid` / `parentUuid` | stable shape, volatile presence | Present on message-bearing line types; forms a parent-chain graph. `queue-operation` and `last-prompt` lines lack both. |
| `cwd`, `gitBranch`, `entrypoint`, `userType`, `permissionMode` | volatile presence, stable meaning when present | Present on `user`/`assistant`/`attachment`/`system` lines, absent on `queue-operation`/`last-prompt`. |
| `message.content` shape | volatile, versioned by Anthropic's own message schema, not CC's | Nests provider-shaped content blocks (`text`, tool-use, etc.); a parser must treat this as an opaque nested document, not flatten it. |
| Additive-only fields (`apiBlockIndex`, `effort`, `promptSource`, `hookInfos`, `hookErrors`, `slug`, `isCompactSummary`, and others) | **volatile across point releases, confirmed by direct comparison** | Comparing real captures at `version:2.1.140` vs `version:2.1.261` on this machine (11 point releases apart, same major.minor), the newer capture carries `apiBlockIndex`, `effort`, `promptSource` that the older one lacks entirely, and the older one carries fields (`hookInfos`, `compactMetadata`, `sourceToolAssistantUUID`, and 15 more) that specific message/system subtypes use. No CHANGELOG or schema doc enumerates these; the only ground truth is diffing captures. |

### codex

| Field | Stability | Notes |
|---|---|---|
| `payload.cli_version` (on `session_meta` only) | present, stable field name, ONCE PER SESSION not per line | Real value observed: `0.144.2`. Unlike CC, this is not repeated on every line, so a parser must read the first `session_meta` line before it has a version to dispatch on; a truncated file missing that first line has no version signal at all. |
| outer envelope `{timestamp, type, payload}` | stable across every line observed | Every line, regardless of `type`, has exactly these three top-level keys; all real per-message content lives inside `payload`. |
| `type` (outer) | stable | Enum observed in one real session: `session_meta`, `turn_context`, `world_state`, `event_msg`, `response_item`. |
| `payload.type` (on `event_msg` and `response_item`) | volatile, second-level discriminator | `event_msg.payload.type` seen: `task_started`, `user_message`, `token_count`, `task_complete`. `response_item.payload.type` seen: `message` (with `role` further discriminating `developer`/`user`/`assistant`). Each carries a materially different payload shape keyed off this nested field, which a flat `type` switch would miss. |
| `turn_context.payload.{sandbox_policy,permission_profile,model}` | present, internally structured, no version tag of its own | Nested config objects with their own sub-shapes (`file_system.entries[].path.type`, etc.); no independent version marker inside this substructure, so schema drift here rides on `cli_version` alone. |
| `world_state.payload.state.agents_md.text` | present but is raw prose, not structured data | Carries the operator's actual instruction file contents verbatim; a parser has no schema to validate here beyond "string present," and this is also the field most likely to contain content a fixture or a parser log must never echo back verbatim (see redaction note in the testdata README). |

### opencode

| Field | Stability | Notes |
|---|---|---|
| On-disk shape | **not a transcript file** | State lives in `~/.local/share/opencode/opencode.db` (SQLite): `session`, `message`, `part` tables, each with an opaque `data` JSON column. There is no flat file to tail. Confirmed by inspecting the real local install: `storage/session_diff/*.json` exists but holds diff metadata, not conversation content; the actual session content is exclusively in the three SQL tables. |
| `session.version` | **present, but not reliably a version** | Real captured values on this machine: a semver-looking `1.15.5` for one session and the literal string `local` for another. A parser that assumes `version` parses as semver will crash or silently misroute on the second value; this is the clearest single instability finding in this spike. |
| `message.data.role` | stable enum | `user`, `assistant` observed; each has a different field set (`assistant` carries `parentID`/`modelID`/`providerID`/`tokens`/`cost`/`path`; `user` does not). |
| `part.data.type` | stable enum, per-type shape varies | `text`, `reasoning`, `tool`, `step-start`, `step-finish` observed in one real session; `tool` parts carry `callID`/`state`, `step-finish` parts carry `cost`/`tokens`/`reason`, `step-start` carries nothing but `type`. |
| id formats (`ses_*`, `msg_*`, `prt_*`, `call_*`) | stable prefix convention | Consistent across every row inspected; useful as a coarse sanity check independent of the `version` column. |

## Version-signal identification

- **CC**: yes, an explicit `version` string on (almost) every line. It is
  the harness's own release version (the same string a `--version` flag
  would print), not a dedicated transcript-schema version. Two releases
  sharing a `version` string are guaranteed to share a transcript schema;
  two releases with different `version` strings are not guaranteed to
  differ, but in the one pair this spike could directly diff (11 point
  releases apart) they did differ. Treat `version` as a probe key into a
  table of known-good schema snapshots, not as a semver range to reason
  about structurally.
- **codex**: yes, `cli_version` inside the first `session_meta` line only.
  Same caveat as CC (it is the binary's release version, not an
  independent schema version), plus the added risk that a parser reading a
  transcript stream rather than a whole file first must buffer until it
  has seen `session_meta`, or explicitly refuse input that starts
  mid-session.
- **opencode**: partially. `session.version` exists and is populated, but
  is not reliably parseable as a version (`local` is a real, observed,
  non-semver value). A parser cannot treat an unparseable `version` as an
  error case to reject outright, because `local` is not corrupt data --
  it is what opencode's own development/unreleased build path writes. The
  fallback strategy below treats `local` as its own named variant, not as
  a parse failure.

## Recommended versioned-parser fallback strategy

One shared dispatch shape, three implementations, because the fallback
LOGIC is identical even though the fallback DATA SOURCE is not:

1. **Identify the harness** from the caller's context (which directory is
   being watched, e.g. `~/.claude/projects/*` vs `~/.codex/sessions/**` vs
   opencode's SQLite path) rather than by sniffing content. All three
   formats are structurally similar enough (JSON-per-line, or a `data`
   JSON column) that content-sniffing across all three risks a false
   match; the watch target already disambiguates for free.
2. **Extract the version key** per harness's own rule: CC reads `version`
   off the first parseable line; codex reads `payload.cli_version` off the
   first `session_meta` line and buffers/refuses input that never produces
   one; opencode reads `session.version` off the row being read, treating
   the literal string `local` as a named variant (`VersionLocal`) rather
   than a parse target.
3. **Probe a table of known schema snapshots** for that harness, keyed by
   the extracted version key (exact match first, then a documented
   nearest-known-older fallback within the same harness -- never across
   harnesses). A version key with no exact match and no defined
   fallback range is UNRESOLVED, not silently mapped to whatever happens
   to be the newest known snapshot.
4. **Fail closed on UNRESOLVED, always.** An unresolved version, a version
   key that cannot be extracted at all (truncated `codex` session missing
   `session_meta`; a CC line with a `version` field of the wrong Go type),
   or a row/line that partially decodes but bottoms out on a required
   field must all return a typed hard error up the call stack. None of
   these states get skipped, defaulted, or best-effort-decoded past the
   point of failure. This is what §5.20's fail-closed rule requires here:
   an unknown-format transcript is not a transcript this spike, or the
   parser it informs, is entitled to guess at.
5. **Never invalidate silently.** A version key that resolves but then
   fails to decode a field the snapshot table says is required is a bug in
   the snapshot table (an under-specified schema), not a data problem; it
   should also fail closed and be logged distinctly from "unresolved
   version" so L/S-24.T2's implementer can tell the two failure modes
   apart when they show up in the field.

## Interface contract sketch (ADR text only; not compiled by this spike)

```go
// Package sketch only. L/S-24.T2 owns the real types, names, and package
// location; nothing here is wired to a production caller and nothing here
// should be copied verbatim without re-deciding names against the tree
// L/S-24.T2 actually lands in.

// TranscriptEvent is one decoded unit from any harness's transcript
// source, normalised enough for a caller to reason about across harnesses
// without needing to know which one produced it.
type TranscriptEvent struct {
    Harness       string          // "cc" | "codex" | "opencode"
    SchemaVersion string          // the raw version key this event was decoded under
    Kind          string          // harness-specific line/row type, preserved verbatim
    Raw           json.RawMessage // the original decoded unit, for callers that need more than Kind
}

// TranscriptParser turns one harness's raw transcript source into
// TranscriptEvents, or fails closed.
//
// Implementations differ in what "source" means: for CC and codex it is a
// stream of JSON lines; for opencode it is a set of SQLite rows. The
// interface deliberately does not assume a byte stream, because opencode's
// evidence in this spike shows that assumption is false for one of the
// three harnesses.
type TranscriptParser interface {
    // ExtractVersionKey inspects source far enough to produce the
    // version key this harness uses (see "version-signal identification"),
    // or returns ErrVersionUnresolved if it cannot -- never a guess.
    ExtractVersionKey(source TranscriptSource) (string, error)

    // Parse decodes source under the given version key into
    // TranscriptEvents, or returns a typed error (never a partial result
    // silently missing some events) if any unit fails to decode under
    // that key's known schema snapshot.
    Parse(source TranscriptSource, versionKey string) ([]TranscriptEvent, error)
}

// TranscriptSource abstracts over "a file being tailed" (CC, codex) and
// "a set of DB rows being read" (opencode). This is the one shape change
// this spike's finding forces on the ticket's original assumption.
type TranscriptSource interface {
    // harness-specific; a file reader for CC/codex, a query result
    // iterator for opencode. Left unspecified here on purpose.
}
```

## Fail-closed parse-error behaviour

Per §5.20: an unknown or unresolvable transcript format is a hard error,
never a silent skip, and never a best-effort partial decode presented as a
complete one. Concretely for this subsystem: `ExtractVersionKey` returning
an error, or `Parse` failing partway through a source, must both surface as
a typed error the caller can act on (e.g. stop watching that session and
alert, rather than continuing to tail a source it can no longer make sense
of). A parser that emits zero events and a nil error for input it could not
actually decode is the one behaviour this ADR rules out unconditionally.

## What would invalidate this ADR

This ADR's central claim -- CC and codex support a file-tailer + version-key
dispatch model, opencode does not and needs a DB-reading path instead -- is
falsified by any of:

- opencode shipping a flat, file-based transcript export as a first-class,
  documented feature (rather than the DB-backed model observed here), which
  would let it share the file-tailer path after all.
- CC or codex removing their per-session/per-line version field, or
  changing it to something that no longer round-trips through a
  known-snapshot table (e.g. dropping semver-like values entirely).
- A harness breaking its transcript schema in a way NOT reflected by any
  change to its own release version string -- i.e. two sessions produced by
  builds reporting the identical `version`/`cli_version` value that
  actually decode to different, incompatible shapes. This spike observed
  no such case, but it also only diffed one CC version pair and one codex
  version (no second codex capture was available on this machine to
  compare against). This is the ADR's weakest evidence point and the one
  L/S-24.T2's implementer should re-check first if the field decode logic
  it builds turns out to need frequent tweaks it can't attribute to a
  version bump: it would mean version does not reliably imply schema after
  all, for that harness.

## What this spike does not claim

The ticket's forward note names three tickets (L/S-25.T1, U/S-45.T1,
F/S-12.T5) gated on an owner prerequisite for a "sanitized labeled
transcript corpus." This spike's three ten-line structural fixtures do
**not** satisfy that prerequisite and this ADR makes no claim that they do.
A labeled corpus for training or evaluation needs many more sessions per
harness, spanning more version pairs than the one this spike could diff,
with task-level labels this spike's fixtures were never annotated with.
What is specifically missing, concretely, for whoever picks up that
prerequisite: (1) more than one codex capture to diff against, since this
machine only had sessions from a single `cli_version` available; (2) enough
opencode sessions to confirm whether `local` is the only non-semver
`version` value ever written, or one of several; (3) an explicit labeling
scheme (what a "label" even means for a harness transcript) that this spike
was never scoped to design.
