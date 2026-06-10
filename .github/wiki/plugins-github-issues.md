# Plugin: github-issues

**Type:** DataSource  
**Version:** 0.1.0  
**Ticket:** T-P4-E03-13  

Fetches GitHub Issues (and optionally PRs) from a configured `owner/repo` via the GitHub REST API v3 for RAG ingestion.

## Permissions

| Permission | Value |
|---|---|
| `net` | `["api.github.com"]` |
| `env` | `["GITHUB_TOKEN"]` |
| `fs` | `false` |

## Config (plugin.json)

| Field | Type | Default | Description |
|---|---|---|---|
| `owner` | string | *(required)* | GitHub org or user name |
| `repo` | string | *(required)* | Repository name |
| `state` | string | `"open"` | Issue state filter: `open`, `closed`, or `all` |

Set via `GITHUB_PLUGIN_OWNER`, `GITHUB_PLUGIN_REPO`, `GITHUB_PLUGIN_STATE` env vars at install time.

## DataItem shape

| Field | Source |
|---|---|
| `id` | Issue number (string) |
| `title` | Issue title |
| `body` | Issue body (empty string if null) |
| `url` | `html_url` |
| `labels` | Label names array |
| `created_at` | ISO-8601 UTC |
| `updated_at` | ISO-8601 UTC |
| `source` | `"github-issues:{owner}/{repo}"` |
| `extra_json` | `"{}"` |

## Pagination

Uses GitHub REST page-number pagination (`?page=N&per_page=100`). The cursor is the next page number as a string. When the API returns fewer than 100 items, `next_cursor` is `None`.

## Build

```bash
# Requires wasm32-wasip1 target:
rustup target add wasm32-wasip1
cargo build -p github-issues --target wasm32-wasip1
```

## Test

```bash
# Pure-logic unit tests run on the host target (no WASM required):
cargo test -p github-issues --lib
```

## Security

`GITHUB_TOKEN` is read once and used only in the `Authorization: Bearer` header. It is never included in any `DataItem` field or log output.
