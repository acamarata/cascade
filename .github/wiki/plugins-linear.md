# Plugin: linear

**Type:** DataSource  
**Version:** 0.1.0  
**Ticket:** T-P4-E03-14  

Fetches Linear issues (with state, priority, assignee, labels) via the Linear GraphQL API for RAG ingestion. Pattern follows `github-issues` exactly.

## Permissions

| Permission | Value |
|---|---|
| `net` | `["api.linear.app"]` |
| `env` | `["LINEAR_API_KEY"]` |
| `fs` | `false` |

## Config (plugin.json)

| Field | Type | Default | Description |
|---|---|---|---|
| `team_id` | string | *(optional)* | Linear team UUID to filter by |
| `state_filter` | array | `[]` | State names to include (e.g. `["In Progress","Todo"]`) |

Set via `LINEAR_PLUGIN_TEAM_ID` (optional) and `LINEAR_PLUGIN_STATE_FILTER` (optional, comma-separated) env vars.

## DataItem shape

| Field | Source |
|---|---|
| `id` | Linear issue UUID |
| `title` | Issue title |
| `body` | Issue description (empty string if null) |
| `url` | Linear issue URL |
| `labels` | Label names array |
| `created_at` | ISO-8601 UTC (`createdAt`) |
| `updated_at` | ISO-8601 UTC (`updatedAt`) |
| `source` | `"linear:{team_id}"` or `"linear:default"` |
| `extra_json` | `{"priority":"High","state":"In Progress","assignee":"Alice"}` |

## Priority mapping

| Linear int | Label |
|---|---|
| 0 | `"No Priority"` |
| 1 | `"Urgent"` |
| 2 | `"High"` |
| 3 | `"Medium"` |
| 4 | `"Low"` |

## Pagination

Uses Linear cursor-based pagination (`pageInfo.hasNextPage` + `endCursor`). The cursor is Linear's opaque `endCursor` string. When `hasNextPage` is false, `next_cursor` is `None`.

## GraphQL query

Single batched query — no N+1 patterns:

```graphql
{
  issues(first: 50, after: $cursor, filter: {team: {id: {eq: $teamId}}}) {
    nodes {
      id title description url priority
      state { name }
      labels { nodes { name } }
      assignee { name }
      createdAt updatedAt
    }
    pageInfo { hasNextPage endCursor }
  }
}
```

## Build

```bash
rustup target add wasm32-wasip1
cargo build -p linear --target wasm32-wasip1
```

## Test

```bash
cargo test -p linear --lib
```

## Security

`LINEAR_API_KEY` is read once and used only in the `Authorization: Bearer` header. It is never included in any `DataItem` field or log output.
