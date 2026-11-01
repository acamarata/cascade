# `cascade context`

Session-scope and context-engine commands. This page covers the `facts`
group; `scope`, `slice`, `show`, `sync` and `harness` are described in their
own command help.

## `facts`

Inferred repository facts: values a model proposes for the seven closed
subjects (`languages`, `build_cmd`, `test_cmd`, `lint_cmd`, `layout`,
`ci_presence`, `harness_files`) that the deterministic detectors do not
already cover. Every fact is recorded in a versioned ledger in
`cascade.db`; nothing is overwritten, so every version stays queryable.

```
cascade context facts list    [--repo <path>]
cascade context facts propose [--repo <path>]
cascade context facts accept  <id>
```

All three verbs need a running daemon: the ledger writer and the in-process
model door live in the daemon. Without one they refuse with an actionable
error. `--repo` defaults to the working directory.

### `list`

Prints every stored version of every fact for the repository: id, subject,
fact text, confidence, version, state (`proposed`, `accepted`, `rejected`,
`superseded`), who accepted it and, for a rejection, the reason.

### `propose`

1. Registers the repository (one id for a main checkout, its linked
   worktrees and any symlinked path) and refreshes its inventory.
2. Builds the request content only from tracked root-level candidates:
   `README*`, `go.mod`, `package.json`, `Cargo.toml`, `pyproject.toml`,
   `Makefile` and `.github/workflows/*.yml`. Each candidate goes through the
   ingest admission guard; only the bytes the guard scanned are sent. A
   refused candidate (a secret, an ignored or excluded path) is left out and
   counted in `refused`. Dot-directory paths such as `.github/` are always
   refused by the guard today. Each file is capped at 32 KiB and the request
   at 128 KiB, cut at a line boundary with the truncation noted.
3. Sends exactly one request with task class `extract` at that class's
   default sensitivity tier. Every file sits inside a
   `<repository-data provenance="untrusted-source">` frame and its markup is
   escaped, so file content cannot close the frame or pose as instructions.
4. Records each proposal. A proposal a detector matches is accepted by that
   detector (`detector:languages`, `detector:commands`, `detector:ci`); one
   a detector contradicts is rejected with the reason kept; any other stays
   `proposed` and is never used.

The reply is `{proposed, accepted, rejected, refused}`. The request accepts
only `{repo}`: a tier or any other field is refused.

### `accept <id>`

Accepts the newest proposed version of a fact as a new version, recorded as
`operator:elevated`. The prior accepted version becomes `superseded` and the
new version's `rollback` points at it.

`context.facts.accept` is always elevated: the CLI sends it only through the
attested elevated-call path, and the daemon touches the ledger only after it
holds a verified elevation for this exact call. `accepted_by` is never read
from the request.

### What accepted facts can and cannot do

Accepted facts are inert data. Each one passes through the policy
projection on its own before any consumer sees it; a fact whose text talks
about gates, risk classes, capabilities, egress, permissions or autonomy is
withheld and its id reported as denied. No fact, in any state, can set a
policy field.
