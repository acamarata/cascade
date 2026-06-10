# Cascade Settings Reference

File: `~/.cascade/settings.json`
Schema version: `2` (T-P3-E07-14)

Cascade stores all user preferences in a single JSON file at `~/.cascade/settings.json`. The file is managed by the app and the `get_settings` / `update_settings` IPC commands. You can also edit it by hand — the format is human-readable JSON.

---

## Quick Reference

```jsonc
{
  "schemaVersion": "2",
  "library": {
    "defaultHarnessTargets": []
  },
  "context": {
    "sitesRoot": null
  },
  "projectMap": {
    "phaseRoot": null
  },
  "providers": {
    "anthropic": null,
    "openai": null,
    "google": [],
    "codex": null,
    "defaultRouting": null
  },
  "geminiPool": {
    "keys": [],
    "proxyPort": 3761,
    "enabled": false
  },
  "harnessBridges": {
    "ccConfigPath": null,
    "ocConfigPath": null,
    "codexConfigPath": null
  },
  "hooks": [],
  "scheduledTasks": [],
  "plugins": {
    "enabled": [],
    "config": {}
  },
  "widgets": {
    "positions": {}
  },
  "mcpServers": [],
  "vaultDisplay": {
    "showMasked": false
  },
  "telemetry": {
    "enabled": false,
    "endpoint": null
  }
}
```

---

## Sections

### `library`

Controls default routing of new library items to AI harnesses.

| Field | Type | Default | Description |
|---|---|---|---|
| `defaultHarnessTargets` | `string[]` | `[]` | Harness slugs receiving new items. Empty = all. |

### `context`

| Field | Type | Default | Description |
|---|---|---|---|
| `sitesRoot` | `string \| null` | `null` | Override the default `~/Sites/` root. |

### `projectMap`

| Field | Type | Default | Description |
|---|---|---|---|
| `phaseRoot` | `string \| null` | `null` | Override the phase root path. |

### `providers`

API keys for each AI provider. Keys are plain strings — **do not commit this file if it contains real API keys**.

| Field | Type | Default |
|---|---|---|
| `anthropic` | `string \| null` | `null` |
| `openai` | `string \| null` | `null` |
| `google` | `string[]` | `[]` |
| `codex` | `string \| null` | `null` |
| `defaultRouting` | `string \| null` | `null` |

### `geminiPool`

| Field | Type | Default | Description |
|---|---|---|---|
| `keys` | `string[]` | `[]` | API keys in the rotation pool. |
| `proxyPort` | `number` | `3761` | Proxy daemon listen port. |
| `enabled` | `boolean` | `false` | Enable the Gemini proxy pool. |

### `harnessBridges`

Paths to external harness configuration files.

| Field | Type | Default |
|---|---|---|
| `ccConfigPath` | `string \| null` | `null` |
| `ocConfigPath` | `string \| null` | `null` |
| `codexConfigPath` | `string \| null` | `null` |

### `hooks`

Array of lifecycle hook definitions. Hook execution is handled by the P2/E-02 engine.

```jsonc
{
  "hooks": [
    {
      "id": "on-session-start",
      "event": "session_start",
      "command": "echo 'session started'",
      "enabled": true
    }
  ]
}
```

### `scheduledTasks`

Array of scheduled task entries with cron expressions.

```jsonc
{
  "scheduledTasks": [
    {
      "id": "nightly-index",
      "label": "Nightly index rebuild",
      "cron": "0 2 * * *",
      "command": "cascade index rebuild",
      "enabled": true
    }
  ]
}
```

### `plugins`

| Field | Type | Default | Description |
|---|---|---|---|
| `enabled` | `string[]` | `[]` | Slugs of active plugins. |
| `config` | `object` | `{}` | Per-plugin config keyed by slug. |

### `widgets`

Widget display geometry, keyed by widget slug.

```jsonc
{
  "widgets": {
    "positions": {
      "usage-widget": { "x": 100, "y": 200, "width": 400, "height": 250 }
    }
  }
}
```

### `mcpServers`

MCP server definitions.

```jsonc
{
  "mcpServers": [
    {
      "name": "my-mcp",
      "command": "/usr/local/bin/my-mcp-server",
      "args": ["--port", "8080"],
      "env": { "API_KEY": "..." },
      "enabled": true
    }
  ]
}
```

### `vaultDisplay`

| Field | Type | Default | Description |
|---|---|---|---|
| `showMasked` | `boolean` | `false` | Show masked key previews in the UI. |

### `telemetry`

| Field | Type | Default | Description |
|---|---|---|---|
| `enabled` | `boolean` | `false` | Opt in to anonymous telemetry. |
| `endpoint` | `string \| null` | `null` | Endpoint URL override. |

---

## IPC Commands

| Command | Description |
|---|---|
| `invoke('get_settings')` | Returns the full `CascadeSettings` object with all defaults filled. |
| `invoke('update_settings', { partial: {...} })` | Merges the partial object into the stored settings. Only keys present in `partial` are updated. |

---

## Upgrading from Schema v1

Schema v1 files (with `providers` and `geminiPool` as plain objects) load without modification. The new typed sections are filled from their defaults. The file is upgraded to v2 format on the next `update_settings` call.

---

## Security Note

API keys in `providers` and `geminiPool.keys` are stored as plain strings in schema v2. Vault encryption is planned for P4. Do not commit `settings.json` to version control.
