# CLI Reference

The `cascade` binary combines direct filesystem operations with daemon-backed commands. Commands that need the daemon use its local Unix socket (or the platform equivalent on Windows).

All subcommands accept `-v` (DEBUG logging) and `-vv` (TRACE). Pass `--help` to any subcommand for full flag details.

---

## cascade init

Scaffold an AI-folder directory at an optional target path. The folder defaults to an existing supported AI folder when one is detected, otherwise `.cascade`; the tier label is inferred from the target path.

```sh
cascade init                                    # initialize the current directory
cascade init /path/to/project --accept-defaults # initialize an explicit path
cascade init --provider anthropic --api-key KEY # connect a cloud provider
cascade init --folder .claude                   # force a specific AI folder name
cascade init --dry-run                          # show what would be created
cascade init --force                            # rewrite generated root files
cascade init --system personal                  # install the personal skill suite
```

**Options**

| Flag | Description |
|---|---|
| `[PATH]` | Target directory; defaults to the current working directory. |
| `--accept-defaults` / `--non-interactive` | Accept defaults and allow an existing setup to be healed idempotently. |
| `--folder <FOLDER>` | Force folder name: `.cascade`, `.claude`, `.codex`, `.opencode`, or any name. |
| `--provider <SLUG>` | Connect a provider after scaffolding. Cloud values: `anthropic`, `openai`, `gemini`, `openrouter`, `groq`, `mistral`, `deepseek`, `together`, `cohere`; `local` prints local-model guidance. |
| `--api-key <KEY>` | API key for `--provider`; required for cloud providers and not used for `local`. |
| `--model <ID>` | Preferred model hint written to `config.toml`. With `local` prints download advice. |
| `--force` | Rewrite generated root files and recreate sibling links in an existing AI folder. |
| `--dry-run` | Print what would be created without writing anything. |
| `--json` | Emit a machine-parseable JSON summary on stdout. |
| `--system <SUITE>` | Install the `pews` or `personal` skill suite; otherwise auto-detected. |

Re-running `cascade init --accept-defaults` on an existing setup is safe — it heals missing subdirectories and skips files that already exist.

---

## cascade status

Show daemon state and the cascade tier/symlink summary for the current working directory. JSON output also includes local RAG metrics.

```sh
cascade status
cascade status --json
```

**Options**

| Flag | Description |
|---|---|
| `--json` | Emit output as JSON (includes RAG shard count, indexed docs, last-index duration). |

Exit code `1` when the daemon is not running or a present tier has broken sibling links; otherwise `0`.

---

## cascade resolve

Print the merged cascade context for the current working directory. Walks up from the current directory, merges all `.cascade/CASCADE.md` files across tiers (GCI through PAC), and prints the result.

```sh
cascade resolve
cascade resolve --json      # structured output with tier provenance
cascade resolve --dedup     # skip duplicate lines across tiers
cascade resolve --dir /path/to/project
```

**Options**

| Flag | Description |
|---|---|
| `--json` | Emit a `ResolvedCascade` JSON object with per-tier provenance. |
| `--dedup` | Deduplicate identical lines across tiers (off by default). |
| `--dir <PATH>` | Resolve from this directory instead of the current working directory. |

---

## cascade search

Search the resolved cascade text. The current CLI implementation ranks matching paragraphs by query-term occurrence; even when a daemon socket exists, the CLI path currently falls back to this in-process search.

```sh
cascade search "how do I handle auth"
cascade search "migration patterns" --top 5
cascade search "error handling" --json
cascade search "lessons" --scope memory
```

**Options**

| Flag | Short | Description |
|---|---|---|
| `--top <N>` | `-n` | Maximum results to return (default: 10). |
| `--json` | | Emit results as a JSON array. |
| `--scope <SCOPE>` | | `all` (default) searches the resolved cascade; `memory` searches Markdown files in the nearest `.cascade/memory/`. |

Use the MCP `cascade.search` tool for the daemon's indexed retrieval pipeline. That MCP tool and the `cascade search` CLI command are separate execution paths.

---

## cascade doctor

Diagnose cascade health and report issues. Checks daemon state, symlink integrity, config schema validity, and cross-tier content duplication.

```sh
cascade doctor
cascade doctor --fix       # attempt automatic repairs
cascade doctor --strict    # treat duplication findings as errors (non-zero exit)
```

**Options**

| Flag | Description |
|---|---|
| `--fix` | Create missing `CLAUDE.md` and `AGENTS.md` sibling links for readable tiers. Existing incorrect links/files are reported but not replaced. |
| `--strict` | Treat cross-tier content duplication findings as FAIL rather than WARN. |

---

## cascade verify

Post-init healthcheck gate. Runs six checks and exits 0 when none has `FAIL`; warnings do not fail the command.

```sh
cascade verify
cascade verify --require-daemon    # treat daemon-not-running as FAIL
cascade verify --json
cascade verify --dir /path/to/project
```

**Options**

| Flag | Description |
|---|---|
| `--json` | Emit `{"checks":[...],"ok":bool}` to stdout. |
| `--require-daemon` | Treat daemon-not-running as FAIL rather than WARN. |
| `--dir <PATH>` | Verify from this directory instead of the current working directory. |

Checks: AI folder exists and is readable, cascade resolves to non-empty output, daemon reachable, AI provider available, `config.toml` parses, OS keychain accessible.

---

## cascade import

Lossless migration engine. Imports a legacy `.claude/`, `.opencode/`, or `.codex/` setup into Cascade's `.cascade/` tiers with a coverage ledger and round-trip verification before writing anything.

```sh
cascade import                                     # dry-run from ~/.claude by default
cascade import --from ~/.claude                    # specify source directory
cascade import --harness opencode                  # specify source harness
cascade import --apply                             # write files (only if lossless)
cascade import --report-json                       # emit plan as JSON
cascade import --from-export backup.cascade-archive.tar.gz
```

**Options**

| Flag | Short | Description |
|---|---|---|
| `--from <PATH>` | `-f` | Source directory. When omitted, the default follows `--harness`: `~/.claude` for `claude-code`, `~/.codex` for `codex`, or `~/.opencode` for `opencode` when it exists (otherwise `~/.claude`). |
| `--harness <HARNESS>` | | Source harness: `claude-code` (default), `opencode`, `codex`. |
| `--dest <PATH>` | `-d` | Destination `.cascade/` directory (defaults to `<cwd>/.cascade/`). |
| `--apply` | `-a` | Write files. Refused if the coverage ledger is not lossless. |
| `--report-json` | | Emit the full plan or report as JSON. |
| `--from-export <ARCHIVE>` | | Restore an archive created by `cascade export`; conflicts with `--from`. |
| `--force` | | Overwrite existing files during a `--from-export` restore. |

Default is dry-run: prints the import plan and coverage ledger without writing. With the default `claude-code` harness, the default source is `~/.claude`; choose another source with `--from` or the matching `--harness`. See also `cascade migrate` for a simpler copy.

---

## cascade migrate

Non-destructive file copy from a legacy tool directory to `.cascade/`. Simpler than `cascade import` — no coverage ledger or round-trip verification. It becomes a move only when `--confirm-delete` is supplied.

```sh
cascade migrate
cascade migrate --from claude       # specify source tool
cascade migrate --dry-run           # preview changes
cascade migrate --confirm-delete    # remove source files after copy
cascade migrate --dest /path/.cascade
```

**Options**

| Flag | Description |
|---|---|
| `--from <TOOL>` | Source tool: `claude`, `opencode`, `codex`. Auto-detected if omitted. |
| `--dry-run` | Print the mapping table without writing. |
| `--confirm-delete` | Delete source files after a successful copy. Irreversible. |
| `--dest <PATH>` | Destination `.cascade/` directory. |

---

## cascade configure

Write Cascade-managed values into a harness runtime config file, idempotently and non-destructively.

```sh
cascade configure --harness claude-code              # dry-run; show diff
cascade configure --harness claude-code --apply      # write the file
cascade configure --harness claude-code --path /custom/settings.json
```

**Options**

| Flag | Description |
|---|---|
| `--harness <TARGET>` | Harness to configure: `claude-code` (writes `~/.claude/settings.json`). |
| `--apply` | Write the file. Without this flag, only a diff is printed. |
| `--path <PATH>` | Override the default settings file path. |

Managed content lives under `_cascade_managed` in the target JSON. All other keys are preserved. Re-running is idempotent.

---

## cascade generate-instructions

Generate harness-native instruction files from your cascade hierarchy. For each found tier, Claude Code output goes under `.claude/`; OpenCode gets `.cascade/opencode-instructions.md` plus a tier-root `opencode.json`. The global OpenCode config also receives the Cascade MCP entry.

```sh
cascade generate-instructions                          # generate for all tiers, both harnesses
cascade generate-instructions --harness cc             # Claude Code only
cascade generate-instructions --harness oc             # OpenCode only
cascade generate-instructions --tier prc               # one tier only
cascade generate-instructions --dry-run                # print diff without writing
```

**Options**

| Flag | Description |
|---|---|
| `--harness <HARNESS>` | `cc`, `oc`, or `both` (default: `both`). |
| `--tier <TIER>` | Limit to one tier: `gci`, `pci`, `apc`, `ppc`, `prc`, `pac`, or `all`. |
| `--project <PATH>` | Project path (defaults to current working directory). |
| `--dry-run` | Print a unified diff without writing any files. |

Generation is idempotent: marked instruction files are skipped rather than appended or refreshed, while JSON entries are upserted and unrelated keys are preserved. Remove a generated instruction target before rerunning only when you intentionally want it materialized again from the current source.

---

## cascade daemon

Control the background cascade daemon (`cascaded`).

```sh
cascade daemon start
cascade daemon start --wait       # wait up to 5 seconds for the socket
cascade daemon stop
cascade daemon restart
cascade daemon status
cascade daemon status --json
cascade daemon install          # register as OS background service
cascade daemon uninstall        # remove the OS-level service
cascade daemon service-status   # show whether the OS service is registered and active
cascade daemon service-restart  # unload+reload (macOS/Linux) or stop+run (Windows)
```

`cascade daemon install` registers `cascaded` as a user-scoped service (launchd LaunchAgent on macOS, systemd `--user` unit on Linux, Task Scheduler ONLOGON task on Windows). No admin elevation required.

---

## cascade memory

Read or write `.cascade/memory/` files.

```sh
cascade memory read decisions.md                   # print a memory file
cascade memory write lessons.md --content "text"   # write or overwrite
cascade memory write lessons.md --append           # append from stdin
cascade memory capture "Prefer explicit errors"    # classify and append
```

**Options for `cascade memory write`**

| Flag | Description |
|---|---|
| `--content <TEXT>` | Content to write. Reads from stdin when omitted. |
| `--append` | Append to the file instead of overwriting it. |
| `--dir <PATH>` | Target `.cascade/` directory. Defaults to the nearest `.cascade/` in CWD. |

**Options for `cascade memory read`**

| Flag | Description |
|---|---|
| `--dir <PATH>` | Source `.cascade/` directory. Defaults to the nearest `.cascade/` in CWD. |

The `.md` extension is added automatically if the filename has none.

`cascade memory capture [TEXT]` reads stdin when text is omitted, auto-selects `decisions`, `lessons`, or `patterns`, and appends tags plus a date. Use `--file <FILE>` to override the destination, `--dir <PATH>` to select the cascade, or `--dry-run` to preview.

---

## cascade config

Read or write cascade configuration values. Config lives in `~/.cascade/config.toml` at GCI level and per-directory `config.toml` files at lower tiers.

```sh
cascade config get rag.enabled
cascade config set rag.enabled true
cascade config set telemetry.enabled false --global
cascade config list
cascade config list --global --json
```

`config get` reads the merged global and nearest-project configuration. `config set` writes the nearest project config unless `--global` is supplied. `config list` accepts `--global` and `--json`.

---

## cascade inbox

Manage `.cascade/inbox/` messages.

```sh
cascade inbox list
cascade inbox send /path/to/project/.cascade "check your auth rules" --priority high --body "Please review the current policy."
cascade inbox archive /path/to/project/.cascade/inbox/message.md
```

`send` requires a target `.cascade` directory and a subject. Optional flags are `--type <TYPE>` (default `info`), `--priority <PRIORITY>` (default `medium`), and `--body <TEXT>`. `archive` takes the message-file path. There is no `inbox clear` subcommand.

`inbox list --tier <TIER>` is accepted by the current parser, but the current implementation still enumerates messages from all visible tiers.

---

## cascade link / unlink

Create or remove a tool-specific symlink pointing to `CASCADE.md`. Lets tools that look for their own config file pick up your cascade context.

```sh
cascade link --tool cursor
cascade unlink --tool cursor
```

Supported tool names are `claude`, `opencode`, `cursor`, `aider`, `codex`, and `continue`. Both commands accept `--dir <PATH>`; `link` also accepts `--force`. Links are created inside the selected or nearest `.cascade/` directory.

---

## cascade template

Manage context templates.

```sh
cascade template list
cascade template apply --id <ID>
cascade template diff --id <ID>
cascade template upgrade --id <ID>
cascade template create --id <ID> --tier prc
cascade template validate /path/to/template.md
cascade template export --id <ID> --output /path/to/dir
```

`apply`, `diff`, and `upgrade` use `--target <PATH>` to override the nearest `CASCADE.md`; write-capable operations offer `--dry-run` and/or `--force` as shown by their command help. `list` supports tier, stack, shape, upgradeable, and JSON filters.

---

## cascade backup / rollback / snapshot

```sh
cascade backup list GCI
cascade backup list GCI --backup-root /path/to/backups
cascade backup schedule daily

cascade rollback list
cascade rollback apply <SNAPSHOT_ID>
cascade rollback apply <SNAPSHOT_ID> --yes

cascade snapshot list
cascade snapshot restore <SNAPSHOT_ID>
cascade snapshot restore <SNAPSHOT_ID> --apply
```

`backup` currently lists daemon-created tier snapshots and writes the requested `daily`, `weekly`, or `hourly` schedule to global config; daemon execution of that schedule is still pending. There is no `backup restore` subcommand.

`rollback` lists or applies pre-update snapshots through the running daemon and prompts before applying unless `--yes` (`-y`) is used. `snapshot` manages pre-generation derived-file snapshots; restore is a dry-run unless `--apply` is supplied, and both snapshot subcommands accept `--workspace <PATH>`.

---

## cascade update

Check for and apply daemon updates, or run a one-command full-stack redeploy.

```sh
cascade update check
cascade update apply
cascade update apply --yes
cascade update auto --enable
cascade update auto --disable
cascade update models
cascade update --full
```

`cascade update` with no subcommand defaults to `check`. Check/apply use the running daemon; `apply` prompts unless `--yes` (`-y`) is supplied. `models` downloads and validates the repository roster, compares it with the existing cache (or compiled roster when no readable cache exists), and writes `~/.cascade/models.yaml`; a download failure exits non-zero.

`cascade update --full` is a one-command full-stack redeploy that composes the existing per-component update logic into a single orchestrated pass:

1. **Daemon + CLI binary update** — reuses the `update apply` IPC (download, verify, snapshot, swap).
2. **Codesign** — ad-hoc signs the swapped `cascaded` and `cascade` binaries (macOS-only; degrades cleanly on other platforms). Fails loudly if the `codesign` tool is missing.
3. **launchd kickstart** — `launchctl kickstart -k` the daemon service (macOS-only; falls back to `cascade daemon restart` when not launchd-managed or on non-macOS). Preferred over `unload`/`load` because it atomically kills and relaunches the job.
4. **Models refresh** — reuses the `update models` logic.
5. **Widget re-install** — reuses `cascade widget install` (re-loads the fleet-widget LaunchAgent).
6. **App signature check** — verifies `~/Applications/Cascade.app` with `codesign --verify --deep --strict` if present (macOS-only; health check, not a redeploy).

`--full` cannot be combined with a subcommand. The pipeline is idempotent and safe to re-run. If `apply` fails, the daemon rolls back internally and the pipeline aborts. If `codesign` fails, the pipeline still proceeds to `kickstart` so the daemon is not left down; the codesign failure is surfaced as an error at the end. `models` and `widget` failures are non-fatal warnings.

---

## cascade mcp

MCP server token management and client setup. See [MCP Server](MCP-Server.md) for details.

```sh
cascade mcp token                              # print the current auth token
cascade mcp status                             # show transport endpoints and auth-token status
cascade mcp setup --tool claude-code           # configure Claude Code
cascade mcp setup --tool opencode             # configure OpenCode
cascade mcp setup --tool vscode               # configure VS Code (Continue.dev)
cascade mcp setup --tool claude-desktop       # configure Claude Desktop
cascade mcp setup --all                        # auto-detect and configure all clients
cascade mcp setup --list                       # detect clients without configuring
cascade mcp setup --tool opencode --dry-run    # preview a target without writing
cascade mcp stdio                              # start MCP server in stdio mode
```

The `cascade mcp setup` command writes the Cascade MCP server entry into each tool's config non-destructively. Existing config entries are preserved; only the Cascade entry is upserted. Pass `--remove` to remove the Cascade entry from a config.

Setup targets are `claude-code`, `claude-desktop`, `vscode`, and `opencode`. Additional setup flags are `--http`, `--local` (project-local OpenCode config), and `--global` (global VS Code config); use `--dry-run` before changing a client.

---

## cascade plugin

Inspect, enable, disable, scaffold, test, and authorize WASM plugins. Registry operations use `~/.cascade/plugins/` by default; override it with the global `--plugins-dir <PATH>` flag.

```sh
cascade plugin list
cascade plugin list --json
cascade plugin info <ID>
cascade plugin enable <ID>
cascade plugin disable <ID>
cascade plugin new <NAME> --no-interactive
cascade plugin test --project-dir /path/to/plugin
cascade plugin grant <ID> <CAPABILITY>
cascade plugin revoke <ID> <CAPABILITY>
cascade plugin trust <PUBLISHER> <BASE64_PUBLIC_KEY>
```

The current CLI has no `plugin install` or `plugin uninstall` subcommand. Plugin directories are discovered from the registry directory; `enable` and `disable` toggle a `.disabled` marker.

---

## cascade cache

Inspect and clear daemon caches.

```sh
cascade cache stats
cascade cache stats --json
cascade cache clear --query --yes
cascade cache clear --embed --chunk
cascade cache clear --all --yes
```

`cache clear` requires at least one of `--query`, `--embed`, `--chunk`, or `--all`, and prompts unless `--yes` (`-y`) is used. It never deletes the RAG index.

---

## cascade completions

Print a shell completion script to stdout.

```sh
cascade completions bash >> ~/.bashrc
cascade completions zsh >> ~/.zshrc
cascade completions fish > ~/.config/fish/completions/cascade.fish
cascade completions powershell | Out-String | Invoke-Expression
```

---

## cascade uninstall

Remove cascade artifacts. Optionally restore archived tool files and delete `~/.cascade/`.

```sh
cascade uninstall
cascade uninstall --keep-cascade --yes
cascade uninstall --full --dry-run
```

The default is `--keep-cascade`: remove service/symlink artifacts but preserve `~/.cascade/`. `--full` also restores archived tool files before removing `~/.cascade/`. Use `--dry-run` to inspect the plan and `--yes` (`-y`) to skip confirmation.

---

## cascade build

Autonomous Build engine — runs a phase to completion, dispatching tickets in topological order via the specified dispatcher.

```sh
cascade build run p2 --mock              # run phase p2 with mock dispatcher (dry-run, no agents)
cascade build run p3 --real              # run phase p3 with real fleet dispatcher (actual agents)
cascade build run --phases /custom/path p2 --mock   # specify custom phases root
```

**Subcommands**

| Subcommand | Description |
|---|---|
| `run <PHASE>` | Run a phase to completion via BuildEngine. Topologically sorts tickets, runs EOSt, EOT, EOS, EOW, EOE, and finally EOP gates. |

**Options**

| Flag | Description |
|---|---|
| `--mock` | Use MockDispatcher (marks tickets done without agent calls). Useful for tests and dry-runs. |
| `--real` | Use FleetDispatcher (dispatches agents to run actual tickets). Requires the full agent-process harness to be operational. |
| `--phases <DIR>` | Explicit phases-root directory. When omitted, Cascade searches upward for `.cascade/phases/` or a bare `phases/` containing `INDEX.yaml`, then falls back to `<cwd>/.cascade/phases/`. |
| `--skip-externals` | Skip external checks (build commands, health probes). Useful for isolated testing. |

Exactly one of `--mock` or `--real` must be specified. The dispatcher determines whether tickets are marked done automatically (mock) or dispatched to external agents (real).

---

## cascade subs

List detected AI coding subscriptions and their auth status.

```sh
cascade subs list            # list all detected subscriptions
cascade subs list --json     # emit as JSON
```

Scans for Claude Code, OpenCode, Codex, Cursor, Antigravity, and Z.ai/GLM Coding Plan, plus discovered provider environment variables. Reports whether each known subscription is installed and authenticated. This is a read-only diagnostic; it does not configure or route inference.

---

## cascade provider

Manage AI provider credentials.

```sh
cascade provider add --kind anthropic --api-key sk-ant-...
cascade provider list
cascade provider remove anthropic
cascade provider test anthropic
```

**Subcommands**

| Subcommand | Description |
|---|---|
| `add --kind <SLUG> --api-key <KEY>` | Validate and store a provider API key in the OS keychain. Short flags: `-k`, `-a`. |
| `list [--json]` | Show connected providers and health status. |
| `remove <SLUG>` | Delete a provider's keychain entry and `providers.json` record. |
| `test <SLUG>` | Live health-check against a connected provider. |

API keys are stored in the OS keychain only. They are never written to disk in plaintext. `add` validates the key against the provider's auth endpoint before any write.

Valid `--kind` values: `anthropic`, `openai`, `gemini`, `openrouter`, `groq`, `mistral`, `deepseek`, `together`, and `cohere`. Local models are managed with `cascade models`, not `cascade provider add`.

---

## Additional top-level commands

The CLI also exposes the following specialized commands. Use `cascade <COMMAND> --help` for their current subcommands and flags.

| Command | Purpose |
|---|---|
| `accounts` | Inspect the fleet account registry, status, routing matrix, and detected accounts. |
| `ccapi` | Manage the default-off experimental Claude Code API proxy bridge. |
| `ceo` | Submit directives and manage status/approvals for the CEO/Founder orchestrator. |
| `check` | Run local content checks such as the injection guard. |
| `conductor` | Use the quota-aware multi-account routing engine. |
| `continuity` | Manage session-resume intents. |
| `context` | Clear session fingerprints or clean up expired context records. |
| `dispatch` | Launch a Claude Code or OpenCode subprocess for a repository. |
| `export` | Create a portable `.cascade-archive.tar.gz` from `~/.cascade/`. |
| `folder` | Read, set, or migrate the preferred AI folder. |
| `health` | Run engineering-excellence health checks on a project. |
| `migrate-keys` | Move `GEMINI_API_KEY_*` secrets from `vault.env` into the OS keychain. |
| `models` | List, download, or remove local model weights. |
| `monitor-oc` | Watch OpenCode session logs and append assistant turns to Cascade's session log. |
| `nsentry` | Manage daemon-owned multi-stream nSentry synchronization. |
| `pbd` | Manage phases, epics, waves, sprints, tickets, steps, and EOx gates. |
| `policy` | Evaluate and manage guardrail policies. |
| `ram` | Inspect or trigger the daemon's RAM Guardian. |
| `restore` | Restore archived legacy-tool files to their original paths. |
| `security` | Run injection, secret, dependency, and prelaunch security checks. |
| `sentry` | Manage the legacy launchd-based nSentry synchronization path. |
| `setup-oc` | Configure OpenCode MCP wiring and project instructions. |
| `telemetry` | Enable, disable, or inspect telemetry consent. |
| `widget` | Install, uninstall, or inspect the fleet widget's user service. |
| `wizard` | Run the interactive first-run configuration wizard. |

`harness` and `ping` are hidden diagnostic commands and are intentionally omitted from the public command list.

---

## Global flags

| Flag | Short | Description |
|---|---|---|
| `--verbose` | `-v` | Enable DEBUG logging. Pass twice for TRACE. |
| `--help` | `-h` | Print help for any subcommand. |
| `--version` | `-V` | Print the cascade version. |

---

See also: [Home](Home.md) · [Cascade Concepts](Cascade-Concepts.md) · [Quickstart](Quickstart.md) · [MCP Server](MCP-Server.md)
