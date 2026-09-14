# cascade-claude external-contract fixture provenance

Art.2 (12-QUALITY-CONSTITUTION.md): a contract this plugin does not own is
tested against a fixture captured from the real counterpart, never against
a dialect this package invented for itself. This plugin touches two such
contracts, and they are in different states. Both are stated here rather
than presented as one uniform "goldens" directory.

## 1. MCP protocol revision — REAL COUNTERPART

`v1-goldens/tools_list.golden.json` is copied byte-for-byte from
`internal/mcp/testdata/tools_list.golden.json`, which was captured from the
real `internal/mcp` server over its own wire. It carries the pinned
revision `2026-07-28` (R-14.14; `server.go`'s `MCPProtocolVersion`).

`mcp_test.go` reads the `protocol_version` out of THIS FILE and asserts the
registered server entry carries the same value. The assertion deliberately
does not restate the revision as a literal in the test: a protocol bump
that lands in the server but never reaches the registration entry has to
fail, and it only can if both sides trace back to one captured artifact.

## 2. Harness MCP config file format — NO CAPTURED COUNTERPART (stated gap)

The file this plugin merges into (`<ConfigRoot>/mcp.json`, a top-level
`mcpServers` object keyed by server name) is the harness's own on-disk
configuration format. This repository contains no capture of that file
taken from a real installed harness, and this ticket did not produce one:
the only real instance available is an individual developer's own
configuration, which is neither committable to a public repository nor a
stable counterpart to pin against.

So the shape in `mcp.go` is written to the format as documented, and it is
NOT claimed to be golden-verified. What the tests do prove, without
depending on that shape being right:

- the merge preserves the CONTENT of every top-level key and every sibling
  server entry it did not author, including entries whose shape it has never
  seen (they round-trip as opaque `json.RawMessage`). Formatting is not
  preserved: the document is re-encoded with standard indentation on every
  write, so a merge normalizes the user's whole file layout. Stated here
  because it is a visible change to a file the user owns;
- registration and re-registration converge on identical bytes;
- unregistration removes this plugin's key and nothing else;
- `FuzzCCConfigMerge` drives the decoder with arbitrary untrusted bytes and
  requires a typed error or a clean merge, never a panic.

Those hold whatever the surrounding document contains, which is the
property that actually matters for a file a user owns and edits. Closing
the gap means capturing a real config from an installed harness into
`v1-goldens/` and asserting the written entry against it; until that
capture exists, this paragraph is the honest statement of what is and is
not verified.

## 3. Fuzz corpus

`fuzz/FuzzCCConfigMerge/seed001` seeds the corpus with a realistic merge
input: a config already holding a foreign server entry alongside this
plugin's own, which is the case where "preserve what I do not own" can
actually regress. Package-local corpus path per R-21.266.
