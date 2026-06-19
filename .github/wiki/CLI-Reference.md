# CLI Reference

The `cascade` binary connects to the `cascaded` daemon over a Unix socket (or named pipe on Windows) and operates directly on `.cascade/` directories for file operations.

All subcommands accept `-v` (DEBUG logging) and `-vv` (TRACE). Pass `--help` to any subcommand for full flag details.

---

## cascade init

Scaffold a `.cascade/` directory at the detected or specified tier.

```sh
cascade init                                    # interactive; auto-detects tier
cascade init --accept-defaults                  # non-interactive; use all defaults
cascade init --provider anthropic --api-key KEY # connect a cloud provider
cascade init --folder .claude                   # force a specific AI folder name
cascade init --dry-run                          # show what would be created
cascade init --force                            # overwrite an existing folder
```

**Options**

| Flag | Description |
|---|---|
| `--accept-defaults` / `--non-interactive` | No prompts; deterministic and idempotent. |
| `--folder <FOLDER>` | Force folder name: `.cascade`, `.claude`, `.codex`, `.opencode`, or any name. |
| `--provider <SLUG>` | Connect a cloud provider after scaffolding (e.g. `anthropic`, `openai`, `gemini`, `local`). |
| `--api-key <KEY>` | API key for `--provider`. Required when a cloud provider is given. |
| `--model <ID>` | Preferred model hint written to `config.toml`. With `local` prints download advice. |
| `--force` | Overwrite an existing AI folder without prompting. |
| `--dry-run` | Print what would be created without writing anything. |
| `--json` | Emit a machine-parseable JSON summary on stdout. |

Re-running `cascade init --accept-defaults` on an existing setup is safe — it heals missing subdirectories and skips files that already exist.

---

## cascade status

Show daemon health, index state, and cascade tier summary for the current working directory.

```sh
cascade status
cascade status --json
```

**Options**

| Flag | Description |
|---|---|
| `--json` | Emit output as JSON (includes RAG shard count, indexed docs, last-index duration). |

Exit code `0` if all checks pass; `1` if any check is FAIL.

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

Run a search against the active index. Returns ranked results with source tier and chunk text.

```sh
cascade search "how do I handle auth"
cascade search "migration patterns" --top 5
cascade search "error handling" --json
```

**Options**

| Flag | Short | Description |
|---|---|---|
| `--top <N>` | `-n` | Maximum results to return (default: 10). |
| `--json` | | Emit results as a JSON array. |

When the daemon is running, search uses the daemon's retrieval pipeline. When the daemon is stopped, it falls back to in-process keyword search over the resolved cascade text.

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
| `--fix` | Auto-repair safe issues (rebuild broken symlinks, remove stale files). |
| `--strict` | Treat cross-tier content duplication findings as FAIL rather than WARN. |

---

## cascade verify

Post-init healthcheck gate. Runs six checks and exits 0 only if all pass.

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
cascade import                                     # dry-run; auto-detects source
cascade import --from ~/.claude                    # specify source directory
cascade import --harness opencode                  # specify source harness
cascade import --apply                             # write files (only if lossless)
cascade import --report-json                       # emit plan as JSON
```

**Options**

| Flag | Short | Description |
|---|---|---|
| `--from <PATH>` | `-f` | Source directory. Auto-detects `~/.claude`, `~/.opencode`, `~/.codex` if omitted. |
| `--harness <HARNESS>` | | Source harness: `claude-code` (default), `opencode`, `codex`. |
| `--dest <PATH>` | `-d` | Destination `.cascade/` directory (defaults to `<cwd>/.cascade/`). |
| `--apply` | | Write files. Refused if the coverage ledger is not lossless. |
| `--report-json` | | Emit the full plan or report as JSON. |

Default is dry-run: prints the import plan and coverage ledger without writing. See also `cascade migrate` for a simpler (non-verified) file move.

---

## cascade migrate

Non-destructive file move from a legacy tool directory to `.cascade/`. Simpler than `cascade import` — no coverage ledger or round-trip verification.

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

Generate harness-native instruction files from your cascade hierarchy. Writes `CLAUDE.md`, `AGENTS.md`, and `settings.json` for Claude Code; writes `opencode.json` and `opencode-instructions.md` for OpenCode.

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
| `--tier <TIER>` | Limit to one tier: `gci`, `pci`, `apc`, `ppc`, `prc`, `pac`. |
| `--project <PATH>` | Project path (defaults to current working directory). |
| `--dry-run` | Print a unified diff without writing any files. |

Generation is idempotent: skips files that already contain the Cascade header marker.

---

## cascade daemon

Control the background cascade daemon (`cascaded`).

```sh
cascade daemon start
cascade daemon stop
cascade daemon restart
cascade daemon status
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
cascade memory read decisions.md
cascade memory write lessons.md "lesson text"
cascade memory list
```

---

## cascade config

Read or write cascade configuration values. Config lives in `~/.cascade/config.toml` at GCI level and per-directory `config.toml` files at lower tiers.

```sh
cascade config get rag.enabled
cascade config set rag.enabled true
cascade config list
```

---

## cascade inbox

Manage `.cascade/inbox/` messages.

```sh
cascade inbox list
cascade inbox send "check your auth rules"
cascade inbox clear
```

---

## cascade link / unlink

Create or remove a tool-specific symlink pointing to `CASCADE.md`. Lets tools that look for their own config file pick up your cascade context.

```sh
cascade link --tool cursor
cascade unlink --tool cursor
```

---

## cascade template

Manage context templates.

```sh
cascade template list
cascade template apply <NAME>
cascade template diff <NAME>
cascade template upgrade
```

---

## cascade backup / rollback / snapshot

```sh
cascade backup list
cascade backup restore <SNAPSHOT_ID>

cascade rollback list
cascade rollback apply <SNAPSHOT_ID>

cascade snapshot list
cascade snapshot restore <SNAPSHOT_ID>
```

`rollback` snapshots are created automatically before every `cascade update apply`. `backup` is a general-purpose snapshot tool. `snapshot` captures pre-generation derived-file state.

---

## cascade update

Check for and apply daemon updates.

```sh
cascade update check
cascade update apply
```

---

## cascade mcp

MCP server token and client setup. See [MCP Server](MCP-Server.md) for details.

```sh
cascade mcp token generate
cascade mcp token revoke <TOKEN_ID>
cascade mcp token list
```

---

## cascade plugin

Manage installed WASM plugins.

```sh
cascade plugin list
cascade plugin install ./my-plugin.wasm
cascade plugin uninstall <NAME>
```

---

## cascade cache

Inspect and clear daemon caches.

```sh
cascade cache stats
cascade cache clear
```

---

## cascade completions

Print a shell completion script to stdout.

```sh
cascade completions bash >> ~/.bashrc
cascade completions zsh >> ~/.zshrc
cascade completions fish > ~/.config/fish/completions/cascade.fish
```

---

## cascade uninstall

Remove cascade artifacts. Optionally restore archived tool files and delete `~/.cascade/`.

```sh
cascade uninstall
```

---

## Global flags

| Flag | Short | Description |
|---|---|---|
| `--verbose` | `-v` | Enable DEBUG logging. Pass twice for TRACE. |
| `--help` | `-h` | Print help for any subcommand. |
| `--version` | `-V` | Print the cascade version. |

---

See also: [Cascade Concepts](Cascade-Concepts.md) · [Quickstart](Quickstart.md) · [MCP Server](MCP-Server.md)
