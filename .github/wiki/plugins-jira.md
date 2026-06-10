# Plugin: jira

**Type:** DataSource  
**Version:** 0.1.0  
**Ticket:** T-P4-E03-15  

Fetches Jira Cloud issues via the Jira REST API v3 (POST /rest/api/3/search with JQL) and normalises them into `DataItem` records for RAG ingestion.

## Permissions

| Permission | Value |
|---|---|
| `net` | `["https://*.atlassian.net"]` |
| `env` | `["JIRA_EMAIL", "JIRA_TOKEN", "JIRA_BASE_URL", "JIRA_JQL", "JIRA_MAX_RESULTS"]` |
| `fs` | `false` |

## Config (env vars set by host at install time)

| Variable | Required | Default | Description |
|---|---|---|---|
| `JIRA_BASE_URL` | yes | — | Jira Cloud base URL (e.g. `https://mycompany.atlassian.net`) — must use https |
| `JIRA_EMAIL` | yes | — | Atlassian account email for Basic auth |
| `JIRA_TOKEN` | yes | — | Jira API token (never logged) |
| `JIRA_JQL` | no | `assignee = currentUser() ORDER BY updated DESC` | JQL query string (must not be empty) |
| `JIRA_MAX_RESULTS` | no | `50` | Issues per page (1–100) |

## Auth

Uses HTTP Basic auth: `Authorization: Basic base64(JIRA_EMAIL:JIRA_TOKEN)` as specified by RFC 7617. Base64 uses the STANDARD alphabet (RFC 4648 §4) with no line wrapping. `JIRA_TOKEN` is consumed only in the auth header and is never stored, logged, or included in any `DataItem` field.

## DataItem shape

| Field | Source |
|---|---|
| `id` | Issue key (e.g. `PROJ-42`) |
| `title` | `fields.summary` |
| `body` | `fields.description` (ADF text extracted recursively) |
| `url` | `{base_url}/browse/{key}` |
| `labels` | `fields.labels` (string array) |
| `created_at` | `fields.created` ISO-8601 UTC |
| `updated_at` | `fields.updated` ISO-8601 UTC |
| `source` | `"jira:{base_url}"` |
| `extra_json` | JSON object with `status`, `issuetype`, `priority`, `assignee`, `reporter` (all strings) |

`issuetype` and `priority` are mapped from the Jira `name` field — they are plain strings, not nested objects.

## Pagination

Uses `startAt` offset pagination. The cursor is the stringified integer offset for the next page (e.g. `"50"`). Returns `next_cursor: None` when `startAt + fetched >= total`.

## Build

```bash
# Requires wasm32-wasip1 target:
rustup target add wasm32-wasip1
cargo build -p jira --target wasm32-wasip1
```

## Test

```bash
# Pure-logic unit tests run on the host target (no WASM required):
cargo test -p jira --lib
```

## Security

- `JIRA_TOKEN` is read once and used only in the `Authorization: Basic` header. It is never included in any `DataItem` field, log line, or error message.
- `JIRA_BASE_URL` is validated to use `https://` at fetch time to prevent token leakage over plain HTTP.
- `JIRA_JQL` is validated to be non-empty to prevent unbounded result sets.
