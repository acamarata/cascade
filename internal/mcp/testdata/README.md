# internal/mcp testdata — provenance

## The goldens are real, and that is the whole point

`goldens/claude-code/2.1.273/` holds an MCP session captured from the
**installed Claude Code 2.1.273** driving the built `cascade` binary, with
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

`goldens_test.go` replays `in.jsonl` and compares against `out.jsonl` frame
by frame. One field is not compared literally: `serverInfo.version` is the
build stamp, so it is asserted equal to `buildinfo.Version` instead of
frozen — stricter than skipping it, since a wrong value still fails.

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

```
cascade mcp serve --stdio --capture <dir>     # via a throwaway .mcp.json
claude -p --mcp-config <file> "list the cascade tools"
```

Copy `<dir>/{in,out}.jsonl` into a directory named for the client version.
Never hand-edit a golden: if the bytes changed, either the server changed
or the client did, and both are things a person should look at.
