<!-- cascade:generate-instructions digest=sha256:44ec44f2e671cc139095ce961c6c7630861bb118dc0c818f44f8a2bb01e57d80 -->
## Cascade Context — PRI Tier (Per-Repo Instructions)

**MCP server:** `stdio: cascade mcp stdio`

Call `cascade.search` before responding to queries about this project.
Call `cascade.context_slice` to retrieve relevant context from the RAG index.
If the cascade MCP tools are unavailable, run `cascade recall` and `cascade context slice` through Bash instead.

# Per-Repo Instructions (PRI)

Round-trip fixture marker: PRI-ROUNDTRIP.

## Build

Run the test suite before every commit.

<!-- /cascade:generate-instructions -->
