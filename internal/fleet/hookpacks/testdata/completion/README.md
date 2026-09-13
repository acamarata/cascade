# testdata/completion provenance

Tool: this package's own completion-gate wire shape (CompletionHookPayload,
completion_gate.go), posted by the rendered "completion-gate" hook pack's
curl command to the `fleet.sessions.completion_check` JSON-RPC method.
Date authored: 2026-09-13 (authored, not live-captured; see the gap note
below, which repeats testdata/../cc-hook-fixtures/README.md's own honest
disclosure for the identical reason).

## What these two fixtures are, and are not

`task_completed_fixture.json` and `stop_fixture.json` are NOT the harness's
native hook-input JSON (that shape -- `session_id`, `transcript_path`,
`cwd`, `hook_event_name`, plus event-specific fields -- is captured
separately in `testdata/cc-hook-fixtures/*.json`, per this package's own
established "two distinct JSON shapes" split). They are this package's OWN
synthesized CompletionHookPayload shape: exactly the four fields
(`job_id`, `task_id`, `session_id`, `event_type`) the rendered hook
command's curl call constructs from the native hook input plus the
Cascade-dispatched agent's own environment (`$CASCADE_JOB_ID`,
`$CASCADE_TICKET_ID`, `$CASCADE_SESSION_ID` -- the real allowlisted
variables every Cascade-dispatched driver inherits, per
pkg/provider.driverEnvAllowlistBase), never the harness's own dialect.

## Art.2 gap (recorded, not papered over)

No live Cascade-dispatched CC session was available to this builder run to
actually invoke the rendered completion-gate hook end to end and capture
its real curl payload. Both fixtures are authored from this builder's
direct, first-hand knowledge of (a) CompletionHookPayload's own declared
field set (completion_gate.go) and (b) the real CASCADE_JOB_ID/
CASCADE_SOCKET environment convention already shipped and tested
(pkg/provider.driverEnvAllowlistBase) -- a genuinely real mechanism, not an
invented one, but not a live capture either. A future ticket with a live
Cascade-dispatched CC session available should replace these two files
with an actual captured invocation and update this block with the real
job id, session id, and capture date.

`stop_fixture.json` carries an empty `task_id`: a Stop event fires on
session idle, not on a specific ticket's completion, so
`$CASCADE_TICKET_ID` is frequently unset even inside a Cascade-dispatched
session. `task_completed_fixture.json` carries a representative ticket id
to show the populated case.

## Fuzz corpus

`testdata/fuzz/FuzzCompletionHookPayload/seed1` seeds FuzzCompletionHookPayload
with one representative CompletionHookPayload wire-format value, matching
the seed FuzzCompletionHookPayload's own `f.Add` call also adds at
runtime.
