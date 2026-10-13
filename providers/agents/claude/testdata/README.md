# providers/agents/claude/testdata

Fixture provenance for the headless driver (Art.2 real-counterpart evidence). Every capture below
came from the installed harness CLI, version `2.1.273`, on the machine that built the driver. Local
paths, account and connector names, session ids and model output are scrubbed; the shapes, keys
and value types are verbatim.

## Protocol revision 1

Revision `"1"` (`ProtocolRange{Min:"1",Max:"1"}`) is defined by this fixture set. A harness whose
probed init event passes `claude_headless_init.json`'s required keys, whose `claude_code_version`
equals its `--version`, and whose result object passes `protocol/1/result.json`'s required keys is
peer `{"1","1"}`; anything else is refused at runtime with `ErrHarnessIncompatible`.

| File | Captured | Command | What it pins |
|---|---|---|---|
| `claude_headless_init.json` | 2026-09-23 01:45 UTC, by the CR-C reviewer | `claude -p ... --output-format stream-json --verbose --model claude-haiku-4-5-20251001` under a second profile's config dir; the rest of its flag set was not recorded (its `permissionMode` shows `bypassPermissions`) | the system/init event: `type`, `subtype`, `session_id`, `claude_code_version`, `model`, `permissionMode`, `tools[]`, `mcp_servers[{name,status}]`, `plugins[]`. The capture has no `plugin_errors` or `mcp_server_errors` key. Scrubbed: `cwd`, ids, `memory_paths`, `messaging_socket_path`, connector names (`connector-N`) and the tool names derived from them, and the account's own skill, agent and command names (`skill-NN`, `agent-NN`, `command-NN`). Its connectors report `needs-auth`, which the driver refuses (a real refusal case in the tests). |
| `protocol/1/result.json` | 2026-09-23 04:09 UTC | `claude -p "Reply ok." --model claude-haiku-4-5-20251001 --output-format json < /dev/null` (config dir: a second profile; exit 0 after 44 s) | the single `--output-format json` result object: `type`, `subtype`, `is_error`, `session_id`, `total_cost_usd`, `num_turns`, `duration_ms`, `usage`. No `--json-schema` was passed, so `structured_output` is absent. Scrubbed: `session_id`, `uuid`, `result` text. |
| `protocol/1/init_plugins_2_1_288.json` | 2026-10-12 | `claude -p --output-format stream-json --verbose ... -- ping` on `2.1.288`, killed at the init line (throwaway runtime home, OAuth-token env, empty git tree) | the `plugins` member of a live init event: the builtin entries gained a third string key, `source`; `pluginEntry` accepts exactly `name`, `path` and `source`. |
| `protocol/1/version.txt` | 2026-09-22 | `claude --version` | `2.1.273` (the vendor product-name suffix removed). |
| `protocol/1/help-excerpt.txt` | 2026-09-23 | `claude --help` | the complete option blocks for every flag the driver emits or probes: `-p/--print`, `--output-format`, `--verbose`, `--no-session-persistence`, `--session-id`, `--setting-sources`, `--strict-mcp-config`, `--add-dir`, `--permission-prompts`, `--permission-mode`, `--disallowedTools`, `--effort`, `--fallback-model`, `--mcp-config`, `--plugin-dir`, `-n/--name`, `--bare`, `--max-budget-usd`, `--json-schema` (never emitted), `--append-system-prompt`, `-v/--version`. |
| `protocol/1/probe_argv.golden.json` | 2026-09-23 | `claude --help` plus parser checks | `Descriptor.ProbeArgv` (TestProbeArgvGolden). |
| `protocol/1/spawn_argv.golden.json` | 2026-09-23 | derived from the two files above | the W6 (all defaults empty) and populated spawn argv (TestHeadlessSpawnFlagsGolden); the session id is `uuid5(NAMESPACE_URL, ticket id)` computed outside the driver. |
| `tool_policy_mappings.golden.json` | 2026-09-23 | `claude --help` | `Descriptor.ToolPolicyMappings` (TestToolPolicyMappingClaudeGolden). |

## Hidden flags

`--max-turns`, `--append-system-prompt-file` and `--append-subagent-system-prompt-file` do not
appear as their own `--help` entries on 2.1.273 (`--append-system-prompt[-file]` is named in the
`--bare` option text). Each was checked against the parser without a model call:
`claude -p <flag> <value> < /dev/null` exits with the missing-input error ("Input must be provided
either through stdin or as a prompt argument"), while an unknown flag exits with
`error: unknown option`. The same method showed that tokens after a literal `--` are positional
(`claude -p --session-id not-a-uuid -- --version` reports the invalid session id, not the version).
The value of `--setting-sources project` was checked the same way on 2026-09-23 (2.1.273):
`claude -p --setting-sources project --session-id bad -- x` reports the invalid session id, while
`--setting-sources bogus` reports "Invalid setting source".

Parser acceptance does not make a flag supported (R-14.329): `probe_argv.golden.json` lists
`--append-subagent-system-prompt-file` and `--max-turns` under `unsupported_flags`, the probe
no longer passes `--max-turns 1`, and a `SubagentKernelPromptFile` or `MaxTurns` value refuses
the spawn with `ErrHarnessIncompatible` (TestUnsupportedFlagValueRefuses).

## Fuzz seeds

`fuzz/FuzzClaudeResultDecode/` holds hand-written seeds for the result decoder (a compact valid
object, an error result, two objects, a missing key, wrong value types, non-JSON, and a
tool-shaped line before a result). They are derived from `protocol/1/result.json`'s shape, not
captured; the committed capture itself is added as a seed by the fuzz target.

## Integration runs

`driver_integration_test.go` and the conformance factory run only when `CASCADE_TEST_CLAUDE_BINARY`
names the harness; `CASCADE_TEST_CLAUDE_HOME` must then name a 0700 runtime home whose `config/`
child is an authenticated config directory. Unset, both tests skip with an asserted refusal.
