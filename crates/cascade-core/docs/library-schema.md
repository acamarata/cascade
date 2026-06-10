# Cascade Library Item Schema

Version: 1  
Status: Canonical (T-P3-E07-01)

## Overview

The Cascade library stores reusable AI-workflow artifacts in
`~/.cascade/library/` as YAML files. Four item types are defined. Each item
maps to one YAML file at:

```
~/.cascade/library/{item_type}/{slug}.yaml
```

| Item type      | Directory                           |
|----------------|-------------------------------------|
| Prompt         | `~/.cascade/library/prompts/`       |
| Agent          | `~/.cascade/library/agents/`        |
| Persona        | `~/.cascade/library/personas/`      |
| Slash command  | `~/.cascade/library/slash-commands/`|

---

## Base fields (all item types)

| Field            | Type             | Required | Notes                                      |
|------------------|------------------|----------|--------------------------------------------|
| `id`             | string (slug)    | yes      | URL-safe, kebab-case, unique per type      |
| `type`           | enum             | yes      | `prompt` \| `agent` \| `persona` \| `slash_command` |
| `title`          | string           | yes      | Human-readable name                        |
| `description`    | string           | yes      | One-line summary                           |
| `content`        | string           | yes      | The primary instruction / prompt text      |
| `tags`           | string[]         | yes      | Zero or more tags; empty list `[]` allowed |
| `version`        | string (semver)  | yes      | e.g. `"1.0.0"`                             |
| `created_at`     | string (RFC 3339)| yes      | ISO 8601 UTC timestamp                     |
| `updated_at`     | string (RFC 3339)| yes      | ISO 8601 UTC timestamp                     |
| `harness_targets`| string[]         | yes      | Subset of `["cc", "oc", "codex"]`          |
| `metadata`       | map              | no       | Type-specific extra fields (see below)     |

All fields are plain YAML-serialisable (no custom tags or anchors required).
Every field that a Rust serde_yaml struct reads uses standard scalar or
sequence types.

---

## Type-specific metadata fields

### `prompt`

No required metadata fields. Optional:

| Key           | Type   | Notes                         |
|---------------|--------|-------------------------------|
| `model_hint`  | string | Preferred model ID            |
| `max_tokens`  | int    | Suggested max_tokens value    |

### `agent`

| Key     | Type     | Notes                                        |
|---------|----------|----------------------------------------------|
| `tools` | string[] | Tool names the agent is expected to call     |

### `persona`

| Key             | Type   | Notes                                       |
|-----------------|--------|---------------------------------------------|
| `system_prompt` | string | Persona system prompt injected before user  |
| `tone`          | string | e.g. `"formal"`, `"concise"`, `"Socratic"`  |

### `slash_command`

| Key       | Type   | Notes                                         |
|-----------|--------|-----------------------------------------------|
| `trigger` | string | The slash-command string, e.g. `/review-code` |

---

## Directory layout example

```
~/.cascade/library/
├── prompts/
│   └── code-review-checklist.yaml
├── agents/
│   └── pr-reviewer.yaml
├── personas/
│   └── strict-editor.yaml
└── slash-commands/
    └── review-code.yaml
```

---

## Serialisation notes

- Field names are `snake_case` in YAML on disk.
- The Rust serde structs use `#[serde(rename_all = "snake_case")]` for disk
  I/O and `#[serde(rename_all = "camelCase")]` for the Tauri IPC layer.
- `tags` and `harness_targets` serialize as YAML sequences; empty is `[]`.
- `metadata` is an open `serde_yaml::Mapping` / `serde_json::Map` — unknown
  keys are accepted and round-tripped without loss.
- All field names are plain ASCII; no YAML aliases or anchors are used.
