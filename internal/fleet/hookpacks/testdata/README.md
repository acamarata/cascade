# testdata/cc-hook-fixtures provenance

Tool: the harness's native hook input schema.
Version: schema as documented/observed by this ticket's builder at build
time; no version string was captured because no live session was
available (see the gap note below).
Date captured: 2026-09-07 (authored, not live-captured; see below).

## Art.2 gap (recorded, not papered over)

The three fixtures in this directory (`pretooluse.json`, `posttooluse.json`,
`stop.json`) were authored from this builder's own direct, first-hand
knowledge of the real native hook-input JSON schema (`session_id`,
`transcript_path`, `cwd`, `hook_event_name`, plus event-specific
`tool_name`/`tool_input`/`tool_response`/`stop_hook_active` fields) —
this is a genuinely real, externally-known schema, not an invented
dialect. It is NOT a live capture: this builder run had no interactive
harness session available to run and record real hook invocations
against, so there is no session transcript, tool version string, or
timestamp of a real invocation to cite as provenance. This is filed as
an Art.2 defect: a future ticket (or the next build pass with a live
session available) should replace these three files with an actual
captured transcript and update this provenance block with the real
tool/version/date.

## Two distinct JSON shapes in this package — read before editing

The fixtures above capture the harness's own NATIVE hook-input shape,
which is NOT the shape internal/fleet/hookpacks.HookPayload decodes.
HookPayload (types.go) is this daemon's own synthesized JSON-RPC params
shape — exactly six fields (harness, event_type, session_id, pid,
account, timestamp_ms) — that a rendered hook command is expected to
construct FROM the native fields above (plus config-supplied harness/
account/pid) before it ever reaches the daemon. Decoding a native
fixture directly as a HookPayload would fail
(json.Decoder.DisallowUnknownFields rejects `transcript_path`, `cwd`,
`tool_input`, etc.) — that failure is intentional: it is exactly the
allowlist boundary this ticket's redaction requirement describes.
Never widen HookPayload to accept the native shape's extra fields.

## Event types attempted per R-16.6, not present in this run

No live session was available (see the gap above), so no attempt beyond
authoring the three fixtures above was possible this run. Every other
member of the R-16.48 mapping table's event set — SessionStart,
InstructionsLoaded, UserPromptSubmit, PostToolBatch, SubagentStart,
SubagentStop, TaskCreated, TaskCompleted, WorktreeCreate,
WorktreeRemove, PreCompact, PostCompact, SessionEnd — is therefore
listed here as **not-emitted-by-this-harness-version** (an asserted absence
from this capture attempt, not a claim the real harness never emits
them): handler.go's dispatch table still handles every one of them by
symbol (R-16.48 requires the table to be complete regardless of fixture
coverage), and hookpacks.SessionsPack only installs descriptors for the
three types actually backed by a fixture here (PreToolUse, PostToolUse,
Stop), per this ticket's own rule that no event type is supported
without a captured fixture.

## Fuzz corpus

`testdata/fuzz/FuzzHookPayload/seed_hook.json` seeds FuzzHookPayload with
one representative HookPayload wire-format value (the daemon's own
decode target), matching the seed FuzzHookPayload's f.Add call also adds
at runtime.
