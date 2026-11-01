# Harness CLI `--help` captures for the workflows probe

Each fixture is one replayable probe of the claude CLI CLI with the keys
`tool`, `version`, `captured_at`, `command`, `stdout`, `exit_code` and
`synthetic`. The probe tests replay them through `ExecProbe` with a stand-in
`Run`: `--help` returns `stdout` and `exit_code`, and `--version` returns
`<version> (claude CLI)`, the exact shape the installed CLI printed.

| File | Synthetic | What it is |
|---|---|---|
| `unsupported-fixture.json` | no | the real `claude --help` of the installed CLI 2.1.316; its `Commands:` section has no `workflows` command (the word appears once, in the prose of an option description) |
| `supported-fixture.json` | yes | the same output with one `workflows` command line appended to `Commands:`, version 2.1.273 |
| `out-of-range-fixture.json` | yes | the supported output with version 2.1.272, one patch below `MinCLIVersion` |

No installed CLI lists a `workflows` command, so the supported and
out-of-range fixtures are constructed and say so with `synthetic: true`.

## Provenance of the real capture

- Tool: claude CLI CLI (native install)
- Version: 2.1.316 (the plan named 2.1.273; 2.1.316 is what is installed, and its `--help` also lists no `workflows` command)
- Binary sha256: 64c327e9 2b2c5e0f 78c0a980 c5a51812 1e1f0d14 0ca771ca 5dd4488d 1a2dcb3f
  (eight groups of eight hex digits; remove the spaces to compare)
- Capture date: 2026-11-01
- Method: the binary was run twice, `--version` then `--help`, with
  `env -i PATH=/usr/bin:/bin HOME=<fresh empty temp dir>`, a fresh empty
  temp working directory and stdin on `/dev/null`. Both exited 0; the
  temp HOME was still empty afterwards. stdout was stored unchanged.

## Scrub

No credential variable was set. The output holds no user path and no
email address; `TestRealCCWorkflowCapturesScrubbed` fails on a macOS or
Linux home-directory path or an email in any file here.
