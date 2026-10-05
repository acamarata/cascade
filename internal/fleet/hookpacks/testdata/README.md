# testdata/cc-hook-fixtures provenance

Every file in `cc-hook-fixtures/` is a live capture of the harness's own
native hook input, taken from one real non-interactive run. Nothing here was
authored from knowledge of the schema. The earlier hand-authored
`pretooluse.json`, `posttooluse.json` and `stop.json` were replaced by the
captures below, because a fixture set for one session needs one session id.

## Provenance table

`TestSessionsPackInstallsFiveCapturedEvents` reads this table: every
descriptor in `SessionsPack()` must have a fixture file named here with a
client version and a capture date.

| Fixture | Client version | Captured | Event |
|---|---|---|---|
| sessionstart.json | 2.1.273 | 2026-10-04 | SessionStart |
| pretooluse.json | 2.1.273 | 2026-10-04 | PreToolUse |
| posttooluse.json | 2.1.273 | 2026-10-04 | PostToolUse |
| stop.json | 2.1.273 | 2026-10-04 | Stop |
| sessionend.json | 2.1.273 | 2026-10-04 | SessionEnd |

## How they were captured

- Tool: the first-party harness client, version 2.1.273.
- Run: a throwaway project directory whose `.claude/settings.json` installed
  one project-scoped command hook for each of the five events; each hook
  command wrote its stdin verbatim to its own file.
- Command: `claude -p "Use the Bash tool to run exactly: echo cascade-capture
  . Then reply with the single word done." --model haiku --allowedTools Bash`
  run inside that directory, once. All five events fired in that single
  session, so the five files share one `session_id`.
- Edits after capture: only three path-valued fields were rewritten, by
  string replacement, to neutral placeholders (`transcript_path`, `cwd`,
  `scratchpad_dir`), so no personal home path or account directory is
  committed. Every other field is the client's own value, re-indented with
  two spaces. The `session_id` is the random identifier the client
  generated for the throwaway session.
- Not committed: the session transcript the `transcript_path` field names.

## What the captures show

- All five events carry `session_id` and `hook_event_name`.
- `SessionStart` carries `source` and no `prompt_id`; `SessionEnd` carries
  `reason`. Both are new relative to the earlier fixtures.
- The client adds fields between releases (`scratchpad_dir`, `tool_use_id`,
  `last_assistant_message`, `background_tasks`, `session_crons`), which is
  why the `hook-event` command decodes only `session_id` and
  `hook_event_name` and ignores the rest.

## Two distinct JSON shapes in this package, read before editing

The fixtures capture the harness's own NATIVE hook-input shape, which is
NOT the shape `internal/fleet/hookpacks.HookPayload` decodes. `HookPayload`
(types.go) is this daemon's own synthesized JSON-RPC params shape: exactly
six fields (harness, event_type, session_id, pid, account, timestamp_ms)
that `cascade fleet sessions hook-event` constructs FROM the native fields
above before anything reaches the daemon. Decoding a native fixture
directly as a `HookPayload` fails (`DisallowUnknownFields` rejects
`transcript_path`, `cwd`, `tool_input` and the rest); that failure is the
allowlist boundary the redaction requirement describes. Never widen
`HookPayload` to accept the native shape's extra fields.

## Event types not captured

The R-16.48 dispatch table in handler.go handles sixteen event types by
symbol. Only the five above are captured, and `SessionsPack()` installs
exactly those five. Every other event type is not installed until a
capture backs it.

## Fuzz corpus

`testdata/fuzz/FuzzHookPayload/seed_hook.json` seeds FuzzHookPayload with
one representative HookPayload wire-format value (the daemon's own
decode target), matching the seed FuzzHookPayload's f.Add call also adds
at runtime.
