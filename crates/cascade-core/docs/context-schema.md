# Context Session Schema

Version: 1.0  
File location: `.cascade/contexts/{slug}.yaml`  
Module: `cascade_core::context_pack`  
Ticket: T-P3-E07-08

---

## Overview

A **context session** is a named, ordered collection of pinned items (file paths, notes,
URLs) that a user wants to inject into an AI coding session. Sessions are stored as
individual YAML files under `.cascade/contexts/`.

Two roots are supported:

| Root | Path |
|------|------|
| Global | `~/.cascade/contexts/` |
| Project-level | `{project_root}/.cascade/contexts/` |

---

## File Layout

```text
.cascade/contexts/
  my-session.yaml
  another-session.yaml
```

Each filename is `{session.id}.yaml`. The `id` is a lowercase slug with no spaces.

---

## `ContextSession` (root document)

```yaml
id: my-session
title: My Session
createdAt: "2026-06-10T12:00:00Z"
updatedAt: "2026-06-10T12:01:00Z"
items:
  - itemType: file
    path: /src/main.rs
    label: Entry point
  - itemType: note
    content: Remember to check the error handling here
    label: Review note
  - itemType: url
    path: https://docs.rs/tokio
```

| Field | Type | Notes |
|-------|------|-------|
| `id` | `string` | Slug — also the filename |
| `title` | `string` | Human-readable display name |
| `createdAt` | `string (RFC3339)` | ISO 8601 timestamp |
| `updatedAt` | `string (RFC3339)` | ISO 8601 timestamp |
| `items` | `ContextItem[]` | Ordered list; empty array is valid |

All datetime fields are RFC3339 strings (never numeric timestamps).

---

## `ContextItem`

| Field | Type | Notes |
|-------|------|-------|
| `itemType` | `"file" \| "note" \| "url"` | Type discriminator |
| `path` | `string?` | Present for `file` and `url`; absent for `note` |
| `content` | `string?` | Present for `note`; absent for `file` and `url` |
| `label` | `string?` | Optional human-readable label for any item type |

Unknown `itemType` values deserialize as `Unknown` (forward-compatible).

---

## `ContextMeta` (lightweight list item)

Returned by `list_contexts` — no items body.

| Field | Type | Notes |
|-------|------|-------|
| `id` | `string` | Session identifier |
| `title` | `string` | Session title |
| `itemCount` | `number` | Count of items in the session |

---

## IPC Commands

| Tauri command | Rust function | Description |
|---------------|---------------|-------------|
| `contextList` | `context_list` | Scan dir → `ContextMeta[]` |
| `contextGet` | `context_get` | Load session by id → `ContextSession` |
| `contextUpsert` | `context_upsert` | Atomic create-or-replace |
| `contextDelete` | `context_delete` | Idempotent delete |

All commands accept an optional `root` string (project root). When `root` is `null`,
the global `~/.cascade/contexts/` directory is used.

---

## Forward Compatibility

- New `ContextItem` variants added in future schema versions appear as `itemType: "unknown"` to this version.
- New top-level fields on `ContextSession` are silently ignored by `serde_yaml`.
- Never rename or remove existing fields without a schema version bump.

---

## Atomic Write Protocol

Writes use `tmp → rename`:

1. Serialize to YAML string.
2. Write to `{slug}.yaml.tmp` in the same directory.
3. `std::fs::rename` to `{slug}.yaml` (POSIX-atomic).
4. If rename fails, remove the `.tmp` file and return an error.

Partial writes are impossible under this protocol.
