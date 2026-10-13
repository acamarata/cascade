# Claude harness credential variable names: provenance

The claude row of the binding table (`harness_claude.go`) names three
environment variables. None comes from operator configuration.

| Variable | Class | Where it is observed |
|---|---|---|
| the config-directory variable (`VendorConfigDirVar` in `providers/agents/claude`) | config directory | the harness binary's own strings (49 occurrences); the claude driver's `VendorConfigDirVar` |
| `CLAUDE_CODE_OAUTH_TOKEN` | `oauth_token` | the harness binary prints `export CLAUDE_CODE_OAUTH_TOKEN=<token>` as the way to hand the `claude setup-token` token to a process |
| `ANTHROPIC_API_KEY` | `api_key` | `claude --help`, option `--bare`: "Anthropic auth is strictly ANTHROPIC_API_KEY or apiKeyHelper via --settings (OAuth and keychain are never read)" |

Captured 2026-10-13 against harness `2.1.288` (`claude --version`).
Method: `claude --help </dev/null` (read the `--bare`, `--model` and `--effort`
options), `claude setup-token --help` (confirms the subcommand: "Set up a
long-lived authentication token (requires Claude subscription)"), and a
read-only `grep -a -c` of the three names in the installed binary. The
`setup-token` flow itself was not run: it starts an interactive login.

`--model <model>` (alias or full name) and `--effort <level>` (`low, medium,
high, xhigh, max`) are listed by the same `claude --help`; the driver's
closed effort set matches that list.

The earlier fixture `providers/agents/claude/testdata/protocol/1/help-excerpt.txt`
(harness 2.1.273) carries the `--bare` and `--effort` lines.
