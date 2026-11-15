# internal/mcp testdata — provenance

## The goldens are real, and that is the whole point

`goldens/claude-code/2.1.273/` holds an MCP session captured from the
**installed mcp-client-a 2.1.273** driving the built `cascade` binary, with
`cascade mcp serve --stdio --capture <dir>`. Captured 2026-09-16.

| File | What it is |
|---|---|
| `in.jsonl` | every frame the client sent, in order |
| `out.jsonl` | every frame cascade answered with |
| `baseline-in.jsonl` | the client's FIRST frame, from before the fix |
| `baseline-out.jsonl` | what the old wire answered it with: `-32600` |

The captured session is `initialize` → `notifications/initialized` →
`tools/list` → `tools/call`. It ended with the client listing cascade's
four tools and invoking one — the first time any real client has completed
a session against this server.

## The capture-and-replay contract

Every capture lives at `goldens/<client>/<client_version>/`, optionally with
a purpose suffix (`<client_version>-<purpose>`), and holds three files:

| File | What it is |
|---|---|
| `in.jsonl` | every frame the client sent, in order; never edited |
| `out.jsonl` | every frame cascade answered with, one per request |
| `capture.json` | provenance: `client`, `client_version`, `protocol_version`, `captured_at`, `command`, and optional `profile` and `needs` |

Each capture also has one row in the table below.
`replay_harness_test.go` is the harness (test-only code): `ReadCapture` loads a directory, `Replay` drives `in.jsonl`
through the production `Server` and `Dispatch` (one output line per
request, none per notification), `CompareFrames` compares the answers with
`out.jsonl` byte for byte after JSON canonicalisation, and `ReplayAll`
walks every directory holding an `in.jsonl`.
`TestMCPGoldensReplayAllCaptures` runs it over this tree. A directory with
no `capture.json`, no row here or no named client fails; so does a tree
with no capture at all.

One field is not compared literally: `serverInfo.version` is the build
stamp, so the replayed value must equal `buildinfo.Version` and the
recorded one is ignored. That is stricter than skipping it, since a wrong
value still fails.

A capture with `"needs": "daemon"` forwards its tools to a daemon.
`ReplayAll` skips it; the test that owns it replays it against one. An
empty `profile` means the full tool set from before tool profiles.

**What `out.jsonl` proves.** It is a regression golden of this server's
answers, not the conformance evidence. Conformance is the real client
completing the captured session. So a change in what the server answers is
a legitimate re-capture: regenerate `out.jsonl` with
`go test ./internal/mcp/ -run TestMCPGoldensReplayAllCaptures -update-goldens`,
which replays the unchanged `in.jsonl`, and review the diff.

| Capture | Client | Version | Protocol | Captured | Command |
|---|---|---|---|---|---|
| `claude-code/2.1.273` | claude-code | 2.1.273 | 2025-11-25 | 2026-09-16 | `claude -p --mcp-config <file> "list the cascade tools"` |
| `claude-code/delegate` | claude-code | 2.1.273 | 2025-11-25 | n/a | the delegate tool call; `needs: daemon`, replayed by its own tool-spec test |

## Why there is no self-authored fixture any more

There used to be a `tools_list.golden.json` written from a contract's own
text, with a note in this file admitting it did not satisfy Art.2. It has
been deleted. It could not have caught the defect that mattered: it agreed
with the server because both came from the same description.

## The history, because it repeated

1. **R-14.14** pinned a revision and described its wire. The server was
   built to that description: `mcp_method`/`mcp_name` frame fields and a
   `notifications/ack` method. **No client sends any of those.**
2. **R-14.238** diagnosed that correctly — "no off-the-shelf client speaks
   this dialect" — and then described a *different* wire from a *different*
   published revision: `server/discover`, a `_meta` layer carrying
   `io.modelcontextprotocol/protocolVersion`, `2026-07-28`, and `-32022`
   for a legacy `initialize`.
3. **The capture** (2026-09-16, R-14.246) shows the real client sends
   `initialize` first and only, with `protocolVersion` **`2025-11-25`** in
   `params`, no `_meta` at the frame level, and never probes
   `server/discover`. Building to step 2 would have rejected the only
   client that exists — the same symptom, one layer up.

**The rule this produced:** a wire protocol is specified by BYTES FROM A
REAL PEER, never by a description of a specification — including a
description written in a ruling, including one written to correct the
previous description. Capture first, implement second, commit the capture.

## Known gap: tools have no input schema

`tools/list` emits a permissive `{"type":"object","properties":{}}` for
every tool, because `pkg/plugin.ToolSpec` has no schema field to emit.

This is not cosmetic, and the capture proves it: the one `tools/call` in
the session failed with `root must not be empty`, because nothing told the
model that the tool needs a `root` argument, so it sent `{}`. Filed as an
Art.9 defect — the fix is a schema field on the manifest, not an invented
schema here, which would describe arguments no handler reads.

## Re-capturing

Captures never write an operator or profile config. Serve with
`cascade mcp serve --stdio --capture <dir>`, which tees both directions to
`<dir>/{in,out}.jsonl`.

The `claude` client, through a throwaway `.mcp.json` and the fleet-only
logged-in profile (point the client's config directory at
`$CASCADE_CLAUDE_EVAL_CONFIG_DIR` first):

```
claude -p --strict-mcp-config \
  --mcp-config <throwaway file> "list the cascade tools, then call cascade_backup_list"
```

codex, with its normal home and the server passed as overrides (no config
write):

```
codex exec -c 'mcp_servers.cascade.command="<abs cascade>"' \
  -c 'mcp_servers.cascade.args=["mcp","serve","--stdio","--capture","<dir>"]' \
  "list the cascade tools, then call cascade_backup_list"
```

Copy `<dir>/{in,out}.jsonl` into a directory named for the client version,
write its `capture.json`, and add its row above. Never hand-edit a golden:
if the bytes changed, either the server changed or the client did, and both
are things a person should look at.

Scrubbed 2026-09-29: private identifiers replaced by neutral synthetic values (identity fields, account and lane ids, client and host names); values replaced, shapes unchanged.
