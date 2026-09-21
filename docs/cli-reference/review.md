# `cascade review`

Runs the native adversarial reviewer (`internal/review`, via the
`cascade-review` builtin plugin) over a unified diff, at the requested CR
tier. Full engine documentation: [`docs/review.md`](../review.md).

```
cascade review [--diff <file|-|pr-ref>] [--pr <ref>] [--level A|B|C] [--json]
```

Exactly one of `--diff` or `--pr` is required.

## Flags

| Flag | Values | Default | Meaning |
|---|---|---|---|
| `--diff` | a file path, or `-` for stdin | — | the unified diff to review |
| `--pr` | a pull request reference, `owner/repo#number` | — | format is validated for real; the fetch itself is **refused** (see below) |
| `--level` | `A`, `B`, or `C` (case-insensitive) | `B` | CR-A (lightweight), CR-B (peer), or CR-C (adversarial) |
| `--json` | boolean | `false` | emit the versioned JSON envelope instead of human-readable text |

## What it prints

TTY/human mode prints a ranked finding list to stdout:

```
$ cascade review --diff mychange.diff --level B
1. [MAJOR] internal/review/provider.go:4: unchecked error path (per the diff's own TODO)

approved=false findings=1
```

With no findings:

```
$ cascade review --diff mychange.diff --level A
cascade review: no findings (approved=true)
```

With `--json`:

```json
{
  "version": 1,
  "findings": [
    {
      "severity": "major",
      "file": "internal/review/provider.go",
      "line": 4,
      "message": "unchecked error path (per the diff's own TODO)"
    }
  ]
}
```

`version` is this envelope's own wire version (currently `1`), independent
of `internal/output.EnvelopeVersion` — `plugins/review` may not import
`internal/output` (Art.10.2), so this is a local, minimal shape rather than
that package's `Envelope`. `findings` is always present (an empty array,
never omitted or `null`) so a machine consumer sees a uniform shape. The
envelope carries no top-level `approved` field — neither the ticket's OUTPUT
contract nor 07-CLI-COMMAND-TREE §review names one (the human-text renderer
above still reports `approved=…`; only the `--json` wire shape dropped it).

## `--pr`'s format is real; the fetch is refused

`--pr` must be `owner/repo#number` — a malformed reference is a plain usage
error. A well-formed one still refuses, because fetching its diff needs two
things this build does not have: `plugins/review` cannot import
`internal/ci` (Art.10.2 — no host bridge exists for this yet, the way
`internal/plugins/review_wiring.go` bridges the review engine itself), and
`internal/ci`'s only egress class (`ci-poll`) is scoped to GitHub Actions
run/job polling, not fetching a pull request's diff:

```
$ cascade review --pr acamarata/cascade#1234
cascade review: --pr "acamarata/cascade#1234" is a well-formed PR
reference, but this build refuses to fetch its diff: plugins/review
cannot reach internal/ci's GitHub client (Art.10.2 boundary: no host
bridge exists yet, matching internal/plugins/review_wiring.go's pattern
for the review engine itself), and internal/ci's only egress class
("ci-poll") is scoped to Actions run/job polling, not PR-diff fetching;
resolve the pull request to a diff yourself (e.g. `git diff` against its
base) and pass it via --diff
```

No provider dispatch happens for a `--pr` call — the refusal fires before
any network-shaped work. This is the honest degradation, not a silent no-op
or a fabricated resolution: passing `--diff` and `--pr` together is refused
the same way, as a plain usage error.

## Non-interactive

This command never prompts. Every input is a flag, so `CASCADE_NO_INPUT=1`
changes nothing about its behaviour or its output (06-FORGE-SPEC §5.8's
automation-parity criterion is satisfied structurally, matching
[`cascade pa pair`](pa-pair.md)'s identical "never prompts" precedent).

## Routing

The command dispatches through the injected `reviewProvider` seam
(`plugins/review/plugin.go`), wired at process boot to the real
`internal/review.Provider` by `internal/plugins/review_wiring.go`'s
`init()`. That provider reaches the daemon's `conductor.execute` door over
the same `pkg/provider.Client`/`internal/client` seam `cascade run` uses —
never a second, direct provider call. A sensitivity refusal from the router,
or a dial failure when the daemon is unreachable, surfaces to this command
as the identical typed `pkg/cascade` error, with its `Kind` intact (no
downgrade to a generic failure) and a non-zero process exit.

## Mounting

The `cascade-review` manifest declares `provides.commands = ["review"]`
(P1-E25-W5-S52-T5, R-16.58) with `rpc_method = "plugin.review.review"`: the
C/S-05.T7 builtin registry's `NewCobraCommand("cascade-review", "review")`
returns a real, non-stub command whose dispatch reaches this file's
`NewReviewCommand()` for real. `cascade review` IS mounted as a top-level
noun on the shipped binary's cobra root —
`cmd/cascade/review_mount.go`'s `mountReviewCmd`, called from
`root.go`'s `mountSubcommands` next to `mountChatCmd` — closing the
composition-root deviation an earlier build left open (that file's own
header has the full reasoning). The same file registers the daemon
JSON-RPC method `plugin.review.review` against the identical
`reviewProvider` seam, via `plugins/review/plugin.go`'s exported `Review()`.

## Exit status

| Status | Meaning |
|---|---|
| 0 | the review ran and produced a verdict (`approved` may still be `false`) |
| 2 | invalid arguments: neither `--diff` nor `--pr`, both given together, an invalid `--level`, a malformed `--pr` reference, or a `--diff` file that could not be read |
| other | the provider refused (an unattributable diff, a sensitivity refusal, an unreachable daemon, or `--pr`'s diff-fetch refusal); the message names which |
