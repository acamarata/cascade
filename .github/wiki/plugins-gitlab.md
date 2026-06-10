# Plugin: gitlab

**Type:** DataSource  
**Version:** 0.1.0  
**Ticket:** T-P4-E03-16  

Fetches GitLab project issues via the GitLab GraphQL API with cursor-based pagination and normalises them into `DataItem` records for RAG ingestion. Mirrors the Linear plugin pattern (both use GraphQL + `endCursor` pagination).

## Permissions

| Permission | Value |
|---|---|
| `net` | `["https://gitlab.com", "https://*.gitlab.com"]` |
| `env` | `["GITLAB_TOKEN", "GITLAB_URL", "GITLAB_PROJECT_PATH", "GITLAB_STATE"]` |
| `fs` | `false` |

## Config (env vars set by host at install time)

| Variable | Required | Default | Description |
|---|---|---|---|
| `GITLAB_TOKEN` | yes | — | GitLab personal access token with `api` scope (never logged) |
| `GITLAB_PROJECT_PATH` | yes | — | Full project path (e.g. `mygroup/myproject`) |
| `GITLAB_URL` | no | `https://gitlab.com` | GitLab instance URL — must use https |
| `GITLAB_STATE` | no | `opened` | Issue state filter: `opened`, `closed`, or `all` |

## Auth

Uses `Authorization: Bearer GITLAB_TOKEN` header. The token is consumed only in the header and is never stored, logged, or included in any `DataItem` field.

## GraphQL API

Posts to `{GITLAB_URL}/api/graphql`. Uses typed GraphQL variables (`$path`, `$state`, `$cursor`) — no string interpolation into the query body, preventing injection through crafted inputs.

Query: `project(fullPath: $path) { issues(first: 50, after: $cursor, state: $state) { nodes { ... } pageInfo { hasNextPage endCursor } } }`

## DataItem shape

| Field | Source |
|---|---|
| `id` | `iid` (project-scoped issue number) |
| `title` | `title` |
| `body` | `description` (empty string if null) |
| `url` | `webUrl` |
| `labels` | `labels.nodes[].title` flattened to `Vec<String>` |
| `created_at` | `createdAt` ISO-8601 UTC |
| `updated_at` | `updatedAt` ISO-8601 UTC |
| `source` | `"gitlab:{project_path}"` |
| `extra_json` | JSON object with `state` (string), `assignees` (string array), `milestone` (string) |

`labels` is a `Vec<String>` (not `Vec<{title: String}>`).

## Pagination

Uses GraphQL `pageInfo.endCursor` / `hasNextPage`. The cursor is the opaque `endCursor` string from the previous page. Returns `next_cursor: None` when `hasNextPage` is `false`.

## Build

```bash
# Requires wasm32-wasip1 target:
rustup target add wasm32-wasip1
cargo build -p gitlab --target wasm32-wasip1
```

## Test

```bash
# Pure-logic unit tests run on the host target (no WASM required):
cargo test -p gitlab --lib
```

## Security

- `GITLAB_TOKEN` is read once and used only in the `Authorization: Bearer` header. It is never included in any `DataItem` field, log line, or error message.
- `GITLAB_URL` is validated to use `https://` at fetch time to prevent token leakage over plain HTTP.
- GraphQL variables are typed and passed separately from the query string — no injection risk through `project_path` or `state` inputs.
