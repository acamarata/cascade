# Harness transcript fixtures (Art.12 spike, P1-E05-W2-S09-T6)

Redacted samples of the on-disk transcript formats produced by three coding
harnesses: CC, codex, and opencode. Captured to support the stability
analysis in `docs/adrs/ADR-E09T6-harness-transcript-stability.md`. This
directory is the permanent home of these fixtures per the ticket's
fixture-home note; they are not moved or duplicated when the parser they
inform (L/S-24.T2) is built — that parser reads these files directly.

## Provenance

All three files were derived from real, locally captured sessions on the
spike operator's own machine, then redacted. None of the three formats was
authored from scratch; where a value was not needed for the stability
question and carried any redaction risk, it was replaced with a placeholder
and that replacement is recorded below.

### cc-sample-redacted.jsonl

- Tool: the CC harness.
- Binary version signal observed: `version` field, values `2.1.140` and
  `2.1.261` (both real, captured from local session history spanning
  2026-08-22 to 2026-09-05).
- Capture date: 2026-09-06 (fixture assembled); source sessions captured
  2026-08-22 and on or after 2026-09-01.
- Command: the file is the harness's own session-log output, written by the
  harness itself to `~/.claude/projects/<encoded-cwd>/<session-id>.jsonl`
  during normal interactive use. No capture command was run by the operator;
  the harness produces this file as a side effect of every session.
- What was redacted: `cwd` -> `<REDACTED_CWD>` (absolute local path); all
  `uuid`/`parentUuid`/`sessionId`/`promptId`/`requestId`/message `id` values
  replaced with synthetic placeholders in the same format; `message.model`
  -> `<REDACTED_MODEL_ID>`; `entrypoint` -> a generic placeholder value; the
  large `agent_listing_delta` / `skill_listing` / `deferred_tools_delta`
  attachment payloads (which enumerate this operator's actual tool and
  skill inventory, hundreds of lines of description text) were truncated to
  one or two representative entries with a `<REDACTED: ... truncated for
  fixture size>` marker in place of the removed text.
- What is preserved: every top-level and nested field name and its type;
  the line-oriented JSON-per-line structure; the four distinct `type`
  values seen in real captures (`queue-operation`, `user`, `attachment`,
  `assistant`, `last-prompt`, `system` -- this fixture samples the first
  five, `system` is documented in the ADR from the same capture but not
  included in the 10-line sample); the parent/child `uuid` chaining shape;
  the fact that `version` is stamped on every per-event line, not once per
  file.
- Real field-set delta this fixture demonstrates: comparing the `2.1.140`
  lines against the `2.1.261` lines in this same file, the newer version
  carries `apiBlockIndex`, `effort`, and `promptSource` fields absent from
  the older capture. This is a real, observed additive delta across two
  point releases 121 patch versions apart on the same major.minor line,
  not a synthesized example.

### codex-sample-redacted.jsonl

- Tool: codex.
- Binary version signal observed: `payload.cli_version` inside the
  `session_meta` line, value `0.144.2` (real, captured 2026-09-06).
- Capture date: 2026-09-06.
- Command: the file is codex's own rollout log, written by the tool itself
  to `~/.codex/sessions/<year>/<month>/<day>/rollout-<timestamp>-<id>.jsonl`
  during normal use (`codex exec`). No separate capture command was run.
- What was redacted: `payload.cwd` and `payload.workspace_roots` ->
  `<REDACTED_CWD>` (absolute local paths, one of which encoded this
  repository's own path); `payload.model_provider` and the assistant-visible
  `model` field -> `<REDACTED_PROVIDER>` / `<REDACTED_MODEL_ID>`;
  `payload.base_instructions.text` and the `world_state` line's
  `payload.state.agents_md.text` were REPLACED WHOLESALE with a short
  `<REDACTED: ... truncated for fixture size>` marker -- these two fields
  hold the full system-instruction text and the operator's own global
  instructions file verbatim in the real capture (thousands of characters,
  containing this operator's real workspace layout and personal tool
  conventions), which is exactly the personal content this redaction rule
  exists to keep out of a public repo; `developer`-role `response_item`
  message text was likewise replaced with a placeholder since it echoes the
  same instruction content; ordinary `user`/`assistant` message text was
  replaced with short generic example strings since the real content was
  this project's own internal planning prose, not needed for the schema
  question and not worth the redaction risk of leaving in.
- What is preserved: the outer envelope shape (`{timestamp, type, payload}`
  on every line, unlike CC's flat per-line schema); the five `type` values
  observed in one real session (`session_meta`, `turn_context`,
  `world_state`, `event_msg`, `response_item`); the fact that `event_msg`
  itself carries a second-level `type` discriminator inside `payload`
  (`task_started`, `user_message`, `token_count`, `task_complete` all seen
  in the same real file); the nested `sandbox_policy` / `permission_profile`
  structure on `turn_context`.

### opencode-sample-redacted.jsonl

- Tool: opencode.
- Binary version signal observed: the `session` table's `version` column.
  Real captured values on this machine: a semver-looking `1.15.5` AND the
  literal string `local` for other sessions. This is itself a stability
  finding, not an artifact of this fixture -- see the ADR.
- Capture date: 2026-09-06.
- Command and an important caveat: **opencode does not write a
  line-oriented transcript file to disk.** Its session state lives in a
  SQLite database at `~/.local/share/opencode/opencode.db`, in `session`,
  `message`, and `part` tables, each storing a JSON blob in a `data`
  column keyed by a stable id. This fixture is therefore a JSONL EXPORT
  this spike produced from real rows in that database, not a native
  on-disk file opencode ever writes itself. The export was produced with:
  ```sql
  SELECT id, data FROM session  WHERE id = '<session-id>';
  SELECT id, data FROM message  WHERE session_id = '<session-id>' ORDER BY time_created;
  SELECT id, data FROM part     WHERE session_id = '<session-id>' ORDER BY time_created;
  ```
  run against the operator's real local database on 2026-09-06, one row
  per exported JSON object, tagged with an `export_kind` field
  (`session`/`message`/`part`) this spike added so the export is
  self-describing; `export_kind` is not a real opencode field.
- What was redacted: `directory` / `path.cwd` / `path.root` ->
  `<REDACTED_CWD>`; `model` / `modelID` -> `<REDACTED_MODEL_ID>`;
  `providerID` -> `<REDACTED_PROVIDER>`; all `session`/`message`/`part`
  ids replaced with synthetic placeholders in the same id-prefix format
  (`ses_`/`msg_`/`prt_`/`call_`) the real database uses; message and part
  text content replaced with short generic example strings (the real rows
  held this project's own internal planning prompts).
- What is preserved: the `data` JSON blob's real internal shape for each
  row kind (`session`, and `message` for both `user` and `assistant`
  roles, and `part` for `text`, `step-start`, `tool`, and `step-finish`
  types -- all five part/message shapes are drawn from real rows queried
  on 2026-09-06); the nested `tokens.cache.{read,write}` shape; the
  `parentID` link from an assistant message to its user message.

## What this fixture set does NOT establish

Per the ticket's own scope note: this directory satisfies THIS spike's
Art.2 evidence requirement only. It does not by itself satisfy the
separate owner prerequisite for a "sanitized labeled transcript corpus"
that gates L/S-25.T1, U/S-45.T1, and F/S-12.T5 -- those tickets need a
labeled, multi-session, multi-version corpus sized for training or
evaluation, not three ten-line structural samples. See the ADR's closing
section for what is specifically missing.
