# MCP fixture provenance

MCP JSON-RPC 2.0 is an external protocol, so Art.2 requires the round-trip
test to run against bytes from a real peer rather than a dialect this repo
wrote for itself. R-14.246 states the standing rule that produced this
directory: *a wire protocol is specified by bytes from a real peer, never by
a description of a specification — including a description written in a
ruling.* Two earlier rounds of cascade's MCP server were built from
paraphrase and spoke a dialect no client understood.

## What is here

| File | What |
|---|---|
| `claude-code-2.1.273-in.jsonl` | every frame the client sent, in order |
| `claude-code-2.1.273-out.jsonl` | every frame the server sent back |

## Provenance

| | |
|---|---|
| **Tool** | Claude Code (`clientInfo.name` = `claude-code`) |
| **Version** | 2.1.273 (`clientInfo.version`, as the client reported it) |
| **Date** | 2026-09-20 |
| **Protocol** | `2025-11-25`, as negotiated in the captured `initialize` |
| **Captured by** | `cascade mcp serve --stdio --capture <dir>` (`cmd/cascade/mcp_capture.go`, P1-E04-W4-S86-T1) |
| **Ticket** | P1-E20-W5-S43-T4 |

## How it was produced

The server was launched by the client itself, through the client's own MCP
configuration, against a cascade daemon on a throwaway `CASCADE_HOME`. The
client was then asked to call `cascade_cpa_send` and `cascade_cpa_history`.
Nothing in these files was written by hand: the `_meta.claudecode/toolUseId`
and `progressToken` fields on each `tools/call`, and the
`content: [{"type":"text", …}]` result blocks, are the client's and the
server's own bytes.

The session is complete and in order: `initialize` →
`notifications/initialized` → `tools/list` → `tools/call` (`cascade_cpa_send`)
→ `tools/call` (`cascade_cpa_history`).

## What is NOT here

No `cascade_cpa_search` call. The harness was asked for the two tools whose
results it could verify in one session; the search tool's own behaviour is
covered by `plugins/cascade-pa/tools/cpa_search_test.go` and by
`FuzzCpaToolInput`. The envelope this fixture pins — the tool-call request
shape and the content-block result shape — is the same for all three, and
`TestCpaMCPRoundTrip` asserts it against the two frames that were actually
recorded rather than inventing a third.

## Identifiers

The capture was scanned before it was committed: it contains no filesystem
paths, usernames, email addresses or credentials. The thread and turn ids in
it were minted by the throwaway daemon for this session and refer to nothing.
