# real-codex: the installed Codex CLI over the Codex config golden

`accepted.json` records one run of the installed Codex CLI's own config
parser over `../golden/harness/codex-config.toml`. `TestCodexRealParserAcceptsConfig`
replays it; no test executes the CLI.

| Field | Value |
|---|---|
| tool | `codex` |
| version | `codex-cli 0.160.0` |
| captured_at | 2026-11-01T10:50:32Z |
| command | `codex mcp list --json` |
| exit_code | 0 |

## Method

1. Two fresh, empty directories under one scratch directory: a HOME and a
   working directory. The environment held only `PATH` and `HOME`; no
   credential variable was set and no network call is needed by the command.
2. The golden was copied byte for byte to `<work>/.codex/config.toml`.
3. Codex reads a project's `.codex/config.toml` only for a trusted project.
   The fake HOME's `.codex/config.toml` therefore held one table, a
   `[projects."<work>"]` entry with `trust_level = "trusted"`, and nothing
   else. Without it the same run lists no server (`[]`), which is the
   expected behaviour for an untrusted directory, not a parse failure.
4. `codex mcp list --json` ran in the working directory. Its stdout is
   `stdout_excerpt`, unchanged. It names no path.

`golden_sha256` is the sha256 of the golden at capture time. A golden
changed without a re-capture fails the replay test, so re-run the steps
above after any change to the Codex leg.

## Scrub

The stdout carries no path, user name or address, so nothing was replaced.
The trust table named the scratch directory and is not committed.
`TestRealHarnessCapturesScrubbed` fails on a macOS or Linux home-directory path or an email
address in any file here.
