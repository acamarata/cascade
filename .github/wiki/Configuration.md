# Configuration Reference

Cascade uses TOML configuration files. The global config lives at `~/.cascade/config.toml`. Each tier directory can have its own `cascade.toml` that adds or overrides settings for that scope only.

Read or write any key from the CLI:

```bash
cascade config get rag.enabled
cascade config set rag.enabled true
cascade config list
```

---

## Daemon settings

```toml
[daemon]
socket_path = "~/.cascade/cascade.sock"
log_level = "info"
shutdown_timeout_secs = 30
```

| Key | Type | Default | Description |
|---|---|---|---|
| `daemon.socket_path` | string | `~/.cascade/cascade.sock` | Unix socket path for IPC |
| `daemon.log_level` | string | `"info"` | Log verbosity: `"error"`, `"warn"`, `"info"`, `"debug"`, `"trace"` |
| `daemon.shutdown_timeout_secs` | integer | 30 | Seconds to wait for clean shutdown before forcing exit |

---

## Index settings

```toml
[index]
root = "~/.cascade"
tier = "auto"
watch = true
debounce_ms = 500
```

| Key | Type | Default | Description |
|---|---|---|---|
| `index.root` | string | `"~/.cascade"` | Root path for the index store |
| `index.tier` | string | `"auto"` | Tier to index: `"auto"` (all active tiers), `"gci"`, `"prc"`, etc. |
| `index.watch` | boolean | `true` | Watch `.cascade/` directories for changes and re-index |
| `index.debounce_ms` | integer | 500 | Milliseconds to wait after a file change before re-indexing |

---

## RAG settings

```toml
[rag]
enabled = true
embedding_model = "bge-m3"
fts_weight = 0.4
dense_weight = 0.6
top_k = 10
```

| Key | Type | Default | Description |
|---|---|---|---|
| `rag.enabled` | boolean | `true` | Whether to build and query the RAG index |
| `rag.embedding_model` | string | `"bge-m3"` | ONNX embedding model for dense search. `"bge-m3"` is bundled. |
| `rag.fts_weight` | float | `0.4` | Weight for FTS5 keyword results in RRF fusion |
| `rag.dense_weight` | float | `0.6` | Weight for dense embedding results in RRF fusion |
| `rag.top_k` | integer | 10 | Number of results to return from `cascade search` |

---

## Tool integrations

```toml
[tools]
claude_code = true
opencode = true
cursor = false
aider = false
windsurf = false
codex = false
antigravity = false
```

| Key | Type | Default | Description |
|---|---|---|---|
| `tools.claude_code` | boolean | `true` | Auto-generate `CLAUDE.md` from merged cascade |
| `tools.opencode` | boolean | `true` | Auto-generate `.opencode/AGENTS.md` from merged cascade |
| `tools.cursor` | boolean | `false` | Auto-generate `.cursorrules` from merged cascade |
| `tools.aider` | boolean | `false` | Auto-generate `.aider.conf.md` from merged cascade |
| `tools.windsurf` | boolean | `false` | Auto-generate `.windsurfrules` from merged cascade |
| `tools.codex` | boolean | `false` | Auto-generate `AGENTS.md` (Codex format) |
| `tools.antigravity` | boolean | `false` | Auto-generate Antigravity config |

Enable a tool in config, then run `cascade generate-instructions` to write its file immediately. The daemon also regenerates these files whenever a `CASCADE.md` changes on disk.

---

## MCP settings

```toml
[mcp]
enabled = true
bind = "127.0.0.1:9762"
auth = "token"
```

| Key | Type | Default | Description |
|---|---|---|---|
| `mcp.enabled` | boolean | `true` | Start the MCP server on daemon launch |
| `mcp.bind` | string | `"127.0.0.1:9762"` | Address and port for the MCP HTTP transport |
| `mcp.auth` | string | `"token"` | Auth mode: `"token"` or `"none"` (local-only environments) |

The MCP server exposes your cascade as live context to any MCP-compatible AI tool. Claude Code picks up the server automatically when `mcp.enabled = true` and the daemon is running. See [MCP Server](MCP-Server.md) for the full tool list.

---

## Backup settings

```toml
[backup]
enabled = true
backup_root = "~/.cascade/backups"
sync_interval_secs = 60
max_versions = 7
```

| Key | Type | Default | Description |
|---|---|---|---|
| `backup.enabled` | boolean | `true` | Enable automatic snapshots |
| `backup.backup_root` | string | `"~/.cascade/backups"` | Where snapshots are stored |
| `backup.sync_interval_secs` | integer | 60 | How often the daemon checks for changes to snapshot |
| `backup.max_versions` | integer | 7 | Maximum number of snapshots to retain per tier |

---

## Plugin settings

```toml
[plugins]
enabled = true
plugin_dir = "~/.cascade/plugins"
sandbox_memory_mb = 64
```

| Key | Type | Default | Description |
|---|---|---|---|
| `plugins.enabled` | boolean | `true` | Load WASM plugins from the plugin directory |
| `plugins.plugin_dir` | string | `"~/.cascade/plugins"` | Directory scanned for `.wasm` files at startup |
| `plugins.sandbox_memory_mb` | integer | 64 | Maximum memory per plugin sandbox in megabytes |

---

## Scheduler settings

```toml
[scheduler]
enabled = false
poll_interval_secs = 30
```

| Key | Type | Default | Description |
|---|---|---|---|
| `scheduler.enabled` | boolean | `false` | Enable the built-in task scheduler |
| `scheduler.poll_interval_secs` | integer | 30 | How often the scheduler checks for due tasks |

---

## Policy settings

```toml
[policy]
enabled = false
policy_dir = "~/.cascade/policies"
```

| Key | Type | Default | Description |
|---|---|---|---|
| `policy.enabled` | boolean | `false` | Enable policy guardrail evaluation |
| `policy.policy_dir` | string | `"~/.cascade/policies"` | Directory where `.policy.toml` files are stored |

Policy guardrails let you write rules that the harness must follow before dispatching actions. For example, you can block reads from certain paths or require a confirmation before writing files. See [Security](Security.md) for the full threat model.

---

## Per-tier `cascade.toml`

Place a `cascade.toml` in any tier directory to override settings for that scope. Settings in a lower-tier file take precedence over the global config for work done in that directory.

Example: raise RAG result count for a specific project:

```toml
# ~/Sites/myproject/.cascade/cascade.toml
[rag]
top_k = 20
```

Example: disable tool generation at the repo level (useful in monorepos where only one app uses AI tools):

```toml
# ~/Sites/myproject/backend/.cascade/cascade.toml
[tools]
claude_code = false
opencode = false
```

---

## Settings app

The Cascade desktop app provides a GUI for all settings. Changes made in the app write directly to `~/.cascade/config.toml`. The [cascade-settings](cascade-settings.md) wiki page documents the full settings schema used by the app layer.

---

See also: [Home](Home.md) · [CLI Reference](CLI-Reference.md) · [Daemon Architecture](Daemon-Architecture.md) · [MCP Server](MCP-Server.md)
