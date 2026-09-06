# AGENTS.md instructions for <WORKSPACE>/app

<INSTRUCTIONS>
<!-- cascade:generate-instructions digest=sha256:68f5df03ba13ed1dc531a463ab650ce029a5c50fe8d24c396e1fa575ee4ac5be -->
## Cascade Context — GCI Tier (Global Cascade Instructions)

**MCP server:** `stdio: cascade mcp stdio`

Call `cascade.search` before responding to queries about this project.
Call `cascade.context_slice` to retrieve relevant context from the RAG index.
If the cascade MCP tools are unavailable, run `cascade recall` and `cascade context slice` through Bash instead.

# Global Cascade Instructions (GCI)

Round-trip fixture marker: GCI-ROUNDTRIP.

## Writing Style

No em dashes. State findings directly.

<!-- /cascade:generate-instructions -->

--- project-doc ---

# Maintainer notes (kept above the managed block)

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

Maintainer notes (kept below the managed block).


<!-- cascade:generate-instructions digest=sha256:5e9854aea75053e2c37b2da8adbe26306c35950016330024a67beca9d69b9ade -->
## Cascade Context — PAI Tier (Per-App Instructions)

**MCP server:** `stdio: cascade mcp stdio`

Call `cascade.search` before responding to queries about this project.
Call `cascade.context_slice` to retrieve relevant context from the RAG index.
If the cascade MCP tools are unavailable, run `cascade recall` and `cascade context slice` through Bash instead.

# Per-App Instructions (PAI)

Round-trip fixture marker: PAI-ROUNDTRIP.

<!-- /cascade:generate-instructions -->

</INSTRUCTIONS>
