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
| `--pr` | a pull request reference, `owner/repo#number` | — | fetches the diff through the trusted `cascade-github` process plugin |
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

## Fetching a pull request

`--pr` must be `owner/repo#number`; malformed references are usage errors.
The host admits the request through the `github-pr-diff` egress class
before launching the trusted `cascade-github` process plugin and calling
its `prs.diff` tool. Install it with `cascade plugin add cascade-github`.

An absent or untrusted plugin, an unavailable process firewall, or a
disabled egress class refuses the fetch. A missing plugin is `unavailable`
and names `cascade plugin add cascade-github`; a disabled or untrusted one
keeps the plugin launcher's `policy_denied` refusal. A diff over 8 MiB is
`invalid_input` ("pull request diff exceeds 8 MiB"), GitHub's 406 is
`unsupported`. A build without the host wiring refuses `unavailable`
("PR diff fetch is not wired in this build"). There is no fallback fetch
path. Errors the plugin returns while running cross the process boundary
as `internal`, with the plugin's message.

Fetched diff bytes reach the review provider with `untrusted-source`
provenance. The reviewer places the filtered diff inside an explicit,
JSON-quoted untrusted-data frame so remote source text stays data.
Local `--diff` requests retain their existing behavior. Passing both
`--diff` and `--pr` remains a usage error.

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
