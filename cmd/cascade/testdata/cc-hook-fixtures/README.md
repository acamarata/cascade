# cc-hook-fixtures provenance

## `userpromptsubmit.json`

A **live capture** of a real UserPromptSubmit hook invocation, made against
the first-party harness client (named by version in the table below; the
same client whose MCP frames `internal/mcp/testdata/goldens` records).

| | |
|---|---|
| Tool | the first-party harness client |
| Version | 2.1.273 |
| Captured | 2026-09-16 |
| Ticket | P1-E16-W4-S34-T4 |
| Method | a throwaway project directory with a project-scoped `UserPromptSubmit` hook whose command appended its stdin verbatim to a file; the client was then run non-interactively against that directory and the resulting bytes are this file, reformatted with `json.dumps(indent=2)` and otherwise untouched. |

Nothing here was authored from knowledge of the schema. The
`prompt_id` and `permission_mode` fields are the reason that distinction
matters: neither appears in this repository's earlier, hand-authored hook
fixtures (`internal/fleet/hookpacks/testdata/cc-hook-fixtures/README.md`
files that gap as an Art.2 defect), and a hook written against those
fixtures would have been written against a schema the client does not
actually send.

### The response side was verified too

The capture ran in both directions. A second run had the hook answer with

```json
{"hookSpecificOutput":{"hookEventName":"UserPromptSubmit","additionalContext":"Cascade context (scope: repository/cascade, 1 items)\n\n- probe capsule line"}}
```

and the session was then asked to repeat back the first line of any
Cascade context block it had been given. It answered:

> Cascade context (scope: repository/cascade, 1 items)

So the injection shape in `context_slice_hook.go` is not a dialect this
repository invented: the real client accepted it and the capsule reached
the model. That is the end-to-end evidence Art.2 asks for, for both halves
of the contract.

### What is deliberately not here

No transcript of the capture session is committed. The payload names a
`transcript_path` under the throwaway config directory the capture used;
the file it points at is not part of this repository and the path is kept
only because it is what the client really sent. The capture used a
disposable config directory in `/tmp`, so no personal path, account or
session identifier from a real working session appears in this file.

### Why this fixture lives here and not beside the other three

`internal/fleet/hookpacks/testdata/cc-hook-fixtures/` holds the payloads
that package's own handler decodes. This one is decoded by
`cmd/cascade/context_slice_hook.go`, which is a different decoder with a
different field set, and a fixture two packages away from its only reader
is one that gets edited for the other reader's benefit.
