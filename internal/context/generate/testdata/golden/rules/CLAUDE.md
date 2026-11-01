<!-- cascade:managed:begin cc-invariants -->
<!-- cascade:generate-instructions digest=sha256:5a45bade7deb020f79ce709dd03bcfd5d582ca625a057808e80880574341acfc -->
## Cascade Context — PRC Tier (Per-Repo Instructions)

**MCP server:** `stdio: cascade mcp serve --stdio`

Call `cascade.search` before responding to queries about this project.
Call `cascade.context_slice` to retrieve relevant context from the RAG index.
If the cascade MCP tools are unavailable, run `cascade recall` and `cascade context slice` through Bash instead.

## Repository invariants

- Blocks between `cascade:managed` markers are generated; edit outside them, never inside.
- Path-scoped commands and facts live in `.claude/rules/`.
- The execution kernel below is also at `.cascade/policy-kernel.md`.
- Continuous integration: `github-actions`.

## Cascade availability

- Call `cascade.search` and `cascade.context_slice` before answering questions about this repository.
- if the Cascade MCP server is unavailable, use the `cascade` CLI for the same operations

<!-- /cascade:generate-instructions -->
<!-- cascade:managed:end -->
<!-- cascade:managed:begin policy-kernel@1 -->
SCOPE: change only what the task names; leave the rest as it is.
SIMPLICITY: write the least code that meets the acceptance.
GROUNDING: read the code or run the tool before you state a fact.
AMBIGUITY: state your assumption, or ask, before you guess.
VERIFICATION: done means the named checks ran and passed.
ESCALATION: after three failed attempts, stop and report what failed.
<!-- cascade:managed:end -->
