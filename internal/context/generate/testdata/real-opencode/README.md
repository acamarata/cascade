# real-opencode: the installed OpenCode CLI over the OpenCode config golden

`accepted.json` records one run of the installed OpenCode CLI's config
loader over `../golden/harness/opencode.json`.
`TestOpenCodeRealParserAcceptsConfig` replays it; no test executes the CLI.

| Field | Value |
|---|---|
| tool | `opencode` |
| version | `1.15.5` |
| captured_at | 2026-11-01T10:50:32Z |
| command | `opencode debug config` |
| exit_code | 0 |

## Method

1. Two fresh, empty directories under one scratch directory: a HOME and a
   working directory. The environment held only `PATH` and `HOME`; no
   credential variable was set.
2. The golden was copied byte for byte to `<work>/opencode.json`.
3. `opencode debug config` ran in the working directory. It prints the
   resolved config; its stdout is `stdout_excerpt`, unchanged. stderr held
   only the first-run database migration notice. The `$schema` member in the
   output is added by the loader; the golden does not carry it.

The loader parses the file as JSONC, so the `//` marker lines are comments
to it. As a control, the same run over a copy of the golden with one closing
brace removed exited 1 with `is not valid JSON(C)`; that run is not committed.

`golden_sha256` is the sha256 of the golden at capture time. A golden
changed without a re-capture fails the replay test, so re-run the steps
above after any change to the OpenCode leg.

## Scrub

The stdout carries no path or address (`username` is the loader's
`unknown` default under the fake HOME), so nothing was replaced.
`TestRealHarnessCapturesScrubbed` fails on a macOS or Linux home-directory path or an email
address in any file here.
