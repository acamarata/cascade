# coretools testdata

## `v1-goldens/tools.json`

The complete MCP tool inventory of Cascade v1, harvested 2026-09-16 for
`P1-E16-W4-S34-T2` (ruling `R-14.251`).

**Provenance.** Extracted from the v1 archive's
`crates/cascade-mcp/src/tool/schemas.rs`, which is the single file where v1
declared every `McpTool` literal: name, description, and `input_schema`. The
extraction read that file and nothing else, and no v1 source was copied into
this tree — the ARCHIVE-MAP evidence rule. Each of the twenty-four entries
records its v1 name, v1 description and v1 input schema exactly as v1
declared them, with descriptions whitespace-normalised (v1 wrote several as
Rust line-continuation string literals, which carry the source file's own
indentation inside the string).

**What the golden is, and is not.** It is the v1 inventory: the list this
build must account for, tool by tool. It is NOT the set of schemas this build
serves. A registered tool's schema describes the params of the v2 RPC method
that answers it, harvested from that method's own Go params type and declared
in `specs.go`. The two are reconciled by a human reading both, never by a
machine diffing them, because a v2 method is under no obligation to take v1's
arguments — and several deliberately do not.

**How it is checked.** `TestMCPToolV1GoldenParity` asserts the equation that
makes this file mean something:

    every v1 tool  ==  registered (specs.go)  +  deferred (deferrals.go)

A v1 tool in neither list fails the test. A deferral with no ticket fails it.
A registered spec whose golden row says `deferred` fails it. The golden cannot
be regenerated from the code to make a failure go away: it is generated from
the v1 archive, and the code is what has to move.

**Seven of twenty-four are registered.** The shape of that gap is stated in
`deferrals.go`'s own doc comment rather than left to a reader to infer: v1's
MCP surface was largely a filesystem API over a project's `.claude` tree, and
v2 does not have that tree. What carried over is what survived the
clean-sheet rewrite — retrieval, context assembly, harness sync, and the
memory record store.
