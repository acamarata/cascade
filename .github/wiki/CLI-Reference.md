# CLI Reference

The `cascade` binary is the primary way to interact with the Cascade system from a terminal. It connects to the `cascaded` daemon over a Unix socket for status and search queries, and operates directly on `.cascade/` directories for file operations.

All subcommands support `-v` (verbose, DEBUG) and `-vv` (trace). Pass `--help` to any subcommand for flag details.

---

## cascade init

Scaffold a `.cascade/` directory at the detected or specified tier.

```bash
cascade init                  # auto-detect tier from cwd
cascade init --tier gci       # force global tier
cascade init --tier prc       # force per-repo tier
```

Flags:
- `--tier <TIER>` - one of `gci`, `pci`, `apc`, `ppc`, `prc`, `pac`
- `--template <NAME>` - apply a named template during init
- `--force` - overwrite an existing `.cascade/` directory

Running `cascade init` with no flags detects the nearest tier by walking up from the current directory. If no cascade directory exists in the hierarchy, it creates one at the PRC (per-repo) level.

---

## cascade status

Show daemon health, index state, and cascade tier summary for the current working directory.

```bash
cascade status
cascade status --json          # machine-readable JSON
```

Output includes:
- Daemon: running / stopped, PID, socket path
- Index: document count, last-indexed timestamp
- Active tiers: which `.cascade/` directories are in scope
- Recent errors (if any)

---

## cascade resolve

Print the merged cascade context for the current working directory. Shows which rules are active and which tier each rule comes from.

```bash
cascade resolve
cascade resolve --format json     # structured output
cascade resolve --tier prc        # show only one tier
```

Useful for debugging which instructions a tool will see. The output mirrors what Cascade writes into derived files like `CLAUDE.md`.

---

## cascade search

Run a RAG search against the active index. Returns ranked results with source tier and file path.

```bash
cascade search "how do I handle auth"
cascade search "Tailwind component structure" --top 5
cascade search "migration patterns" --format json
```

Flags:
- `--top <N>` - number of results to return (default: 10)
- `--format <FORMAT>` - `text` or `json`

Search uses RRF (Reciprocal Rank Fusion) over FTS5 keyword matching and BGE-M3 dense embeddings. Both paths run locally; no data leaves the machine.

---

## cascade inbox

Manage `.cascade/inbox/` messages.

```bash
cascade inbox list                       # list pending messages
cascade inbox send "check your auth"     # send a message to self
cascade inbox clear                      # remove all read messages
```

Subcommands: `list`, `send <MESSAGE>`, `clear`

---

## cascade memory

Read or write `.cascade/memory/` files.

```bash
cascade memory read decisions.md
cascade memory write lessons.md "lesson content"
cascade memory list
```

Subcommands: `read <FILE>`, `write <FILE> <CONTENT>`, `list`

---

## cascade config

Read or write cascade configuration values. Configuration lives in `~/.cascade/config.toml` (GCI-level) and per-directory `cascade.toml` files.

```bash
cascade config get rag.enabled
cascade config set rag.enabled true
cascade config list                  # all keys and current values
```

Subcommands: `get <KEY>`, `set <KEY> <VALUE>`, `list`

---

## cascade link

Create a tool-specific symlink pointing to `CASCADE.md`. This lets tools that look for their own config file (`.cursorrules`, `.aider.conf.md`) pick up your cascade context automatically.

```bash
cascade link --tool cursor
cascade link --tool aider
cascade link --tool windsurf
```

Flags:
- `--tool <TOOL>` - one of `claude-code`, `opencode`, `cursor`, `aider`, `windsurf`, `codex`, `antigravity`

---

## cascade unlink

Remove a tool-specific symlink created by `cascade link`.

```bash
cascade unlink --tool cursor
```

---

## cascade migrate

Migrate a legacy `.claude/` or `.opencode/` directory to `.cascade/`.

```bash
cascade migrate ~/.claude
cascade migrate --dry-run ~/.claude    # preview changes without writing
```

Flags:
- `--dry-run` - show what would change without writing files
- `--keep-original` - do not delete source files after migration

---

## cascade migrate-keys

Move `GEMINI_API_KEY_*` secrets from `vault.env` into the OS keychain (macOS Keychain / Linux Secret Service / Windows Credential Manager).

```bash
cascade migrate-keys
cascade migrate-keys --dry-run
```

---

## cascade doctor

Diagnose cascade health and report issues. Checks daemon state, index coherence, missing symlinks, and config schema validity.

```bash
cascade doctor
cascade doctor --fix      # attempt automatic repairs
```

---

## cascade daemon

Control the cascade background daemon.

```bash
cascade daemon start
cascade daemon stop
cascade daemon restart
cascade daemon status
cascade daemon logs           # tail the daemon log file
```

The daemon runs as a user-session process. It does not require admin elevation. Logs go to `~/Library/Logs/cascade-daemon.log` on macOS and `~/.local/share/cascade/daemon.log` on Linux.

---

## cascade backup

Manage backup snapshots.

```bash
cascade backup list
cascade backup restore <SNAPSHOT_ID>
```

See the [Backup](Backup.md) page for the full snapshot model.

---

## cascade rollback

Manage pre-update snapshots.

```bash
cascade rollback list
cascade rollback apply <SNAPSHOT_ID>
```

Rollback snapshots are created automatically before every `cascade update apply`. Use this if an update breaks something.

---

## cascade update

Check for and apply daemon updates.

```bash
cascade update check
cascade update apply
cascade update auto --enable    # enable background auto-update
cascade update auto --disable
```

---

## cascade template

Manage context templates.

```bash
cascade template list
cascade template apply <NAME>
cascade template diff <NAME>      # preview changes before applying
cascade template upgrade          # upgrade all applied templates to latest
```

See the [Templates](templates.md) page for the full template model.

---

## cascade mcp

MCP server token and client setup.

```bash
cascade mcp token generate
cascade mcp token revoke <TOKEN_ID>
cascade mcp token list
cascade mcp client add <NAME> <URL>
cascade mcp client remove <NAME>
```

See the [MCP Server](MCP-Server.md) page for setup details.

---

## cascade generate-instructions

Generate harness-native instruction files (`CLAUDE.md`, `AGENTS.md`, `settings.json`, `.cursorrules`) from your cascade hierarchy.

```bash
cascade generate-instructions
cascade generate-instructions --tool claude-code
cascade generate-instructions --dry-run
```

---

## cascade setup-oc

Configure OpenCode: MCP wiring and per-project instruction injection.

```bash
cascade setup-oc
cascade setup-oc --project /path/to/repo
```

---

## cascade dispatch

Launch a Claude Code or OpenCode subprocess targeting a repo with optional context injection.

```bash
cascade dispatch --harness cc --repo /path/to/repo
cascade dispatch --harness oc --repo /path/to/repo --inject "focus on auth"
```

---

## cascade monitor-oc

Watch OpenCode session logs and append assistant turns to `.cascade/oc-session-log.md`.

```bash
cascade monitor-oc
cascade monitor-oc --session <SESSION_ID>
```

---

## cascade plugin

Manage installed WASM plugins.

```bash
cascade plugin list
cascade plugin install ./my-plugin.wasm
cascade plugin uninstall <NAME>
cascade plugin inspect <NAME>
```

See the [Plugin Development Guide](plugin-development-guide.md) for building plugins.

---

## cascade cache

Inspect and clear daemon caches.

```bash
cascade cache stats
cascade cache clear
cascade cache clear --type embeddings
```

---

## cascade context

Manage context fingerprints for cross-session dedup.

```bash
cascade context list
cascade context clear-session <SESSION_ID>
cascade context cleanup-expired
```

---

## cascade policy

Manage guardrail policies.

```bash
cascade policy list
cascade policy eval <QUERY>
cascade policy add <FILE>
cascade policy remove <NAME>
```

---

## cascade restore

Restore an archived tool's files to their original paths.

```bash
cascade restore --tool cursor
```

---

## cascade uninstall

Remove cascade artifacts. Optionally restore archived tools and delete `~/.cascade/`.

```bash
cascade uninstall
cascade uninstall --restore-tools
cascade uninstall --purge         # also delete ~/.cascade/
```

---

## cascade completions

Print shell completion script to stdout.

```bash
cascade completions bash >> ~/.bashrc
cascade completions zsh >> ~/.zshrc
cascade completions fish > ~/.config/fish/completions/cascade.fish
```

---

## Global flags

| Flag | Short | Description |
|---|---|---|
| `--verbose` | `-v` | Enable DEBUG logging (repeat for TRACE) |
| `--help` | `-h` | Print help for any subcommand |
| `--version` | `-V` | Print the cascade version |
