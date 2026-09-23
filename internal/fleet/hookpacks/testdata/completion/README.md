# testdata/completion provenance

`task_completed_fixture.json`: this package's own completion-gate wire
shape (CompletionHookPayload, completion_gate.go), authored (not
live-captured; see the gap note below). `stop_fixture.json`: a REAL
captured native harness Stop payload -- see "Round-3 real Stop capture"
below, which supersedes its own earlier self-authored version.

## What these fixtures are, and are not

`task_completed_fixture.json` is NOT the harness's native hook-input JSON
(that shape -- `session_id`, `transcript_path`, `cwd`, `hook_event_name`,
plus event-specific fields -- is captured separately in
`testdata/cc-hook-fixtures/*.json`, and now also in this directory's own
`stop_fixture.json`). It is this package's OWN synthesized
CompletionHookPayload shape: exactly the four fields (`job_id`, `task_id`,
`session_id`, `event_type`) the rendered hook command's curl call
constructs from the native hook input plus the Cascade-dispatched agent's
own environment (`$CASCADE_JOB_ID`, `$CASCADE_TICKET_ID`,
`$CASCADE_SESSION_ID` -- the real allowlisted variables every
Cascade-dispatched driver inherits, per
pkg/provider.driverEnvAllowlistBase), never the harness's own dialect.

## Round-3 real Stop capture (2026-09-23)

Tool: the CC harness CLI, version 2.1.273 (`claude --version`). Method: a
headless `-p` run with a settings file registering a native Stop hook
whose command is `cat > stop.json` (captures the harness's real stdin
payload verbatim, byte-exact field names/shapes). Redactions applied
before landing in this repo: `session_id`/`prompt_id` zeroed to
`00000000-0000-0000-0000-000000000000`; `transcript_path`, `cwd`,
`scratchpad_dir` replaced with `/redacted/<field>`; `last_assistant_message`
replaced with the literal `"ok"`. `stop_hook_active` is real (`false` --
this was a first Stop, not a replay after a block); `background_tasks`
and `session_crons` are real (both empty in this capture).
`stop_fixture.json` is this package's ONLY fixture that is the harness's
actual native wire format rather than CompletionHookPayload's own shape;
completion_hook_command.go's `completionHookCommand` reads it off stdin
directly (real code path, not this test-only decode), and
completion_hook_command_test.go's
`TestCompletionHookCommand_StopHookActiveNeverBypassesFailClosed` drives
it through the rendered shell template. TaskCompleted did NOT fire in
this run (two attempts, same as the D3 note below);
`task_completed_fixture.json` stays self-authored.

## Art.2 gap (recorded, not papered over) -- TaskCompleted only

Stop is now resolved by the round-3 real capture above. No live
Cascade-dispatched CC session was available to this builder run to invoke
TaskCompleted end to end and capture its real payload (two attempts, see
D3 below). `task_completed_fixture.json` is authored from this builder's
direct, first-hand knowledge of (a) CompletionHookPayload's own declared
field set (completion_gate.go) and (b) the real CASCADE_JOB_ID/
CASCADE_SOCKET environment convention already shipped and tested
(pkg/provider.driverEnvAllowlistBase) -- a genuinely real mechanism, not an
invented one, but not a live capture either. A future ticket with a
TaskCompleted-firing session available should replace this one remaining
file and update this block with the real job id, session id, and capture
date.

## CR-B rework capture attempt (2026-09-22, D3)

Tool: `claude` CLI 2.1.273 (`claude --version`). Method: a throwaway
settings file registering native Stop and TaskCompleted hooks whose
command is `cat > <file>` (captures whatever CC writes to the hook's
stdin verbatim), then `claude -p "reply with the word ok" --model
claude-haiku-4-5-20251001 --settings <file> < /dev/null` run once from
inside this build lane's own CC session, plus a second bare
`claude -p ... < /dev/null` invocation (no `--settings`) as a control.
Both invocations returned exit 124 (timeout) after 45-60s with "Error: No
messages returned from query" and no hook fired either time -- including
the control run with no hooks configured at all, so this is a nested-
invocation environment limitation of running `claude -p` from inside an
already-running CC build-lane session, not a defect in the
settings/hook registration this ticket wrote. Repeatable, not a flake.

Neither fixture below could therefore be replaced with a real capture
this run; both stay self-authored, per the disclosure above. This is a
genuine, repeatable attempt (not a repeat of the ORIGINAL gap, which had
literally no live `claude` binary available at all) and the specific
failure mode is recorded here so a future run with a top-level (non-
nested) CC session can pick a different method rather than repeat this
one.

For TaskCompleted specifically: the official hooks documentation text
already in this repo (testdata/README.md's own "Two distinct JSON
shapes" section, and this same file's own note above) states the native
shape as `session_id`, `transcript_path`, `cwd`, `hook_event_name`, plus
event-specific fields -- TaskCompleted's own event-specific fields were
not observed in this run either, for the same reason.

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
