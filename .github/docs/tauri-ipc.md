# Cascade Tauri IPC Reference

The `cascade-app` frontend communicates with the `cascaded` daemon through a fixed set of Tauri commands. Each command is a Rust `#[tauri::command]` function in `src-tauri/src/commands.rs`. The Rust handler opens a JSON-RPC 2.0 call over a Unix domain socket, waits for the daemon response, and returns the deserialized result to the TypeScript caller.

The TypeScript `ipc` singleton (`src/lib/ipc/client.ts`) is the only place in the codebase that calls `invoke` from `@tauri-apps/api/core`. All other code calls `ipc` methods directly. This keeps the Tauri coupling in one file and makes the rest of the frontend testable without a live Tauri context.

The IPC contract is frozen at schema v1. Command names, parameter names, and return-type shapes are append-only. Nothing is renamed or removed once shipped.

---

## Error Handling

All `ipc` methods reject with a `CascadeIpcError` on failure. The error class has two fields:

```typescript
class CascadeIpcError extends Error {
  code: string    // Rust error variant name, e.g. "DaemonNotRunning"
  message: string // Human-readable detail from the daemon
}
```

Tauri serializes Rust errors as either a JSON string `{"code":"...","message":"..."}` or a plain string in `"CodeVariant: detail"` format. The `ipc` client's internal `call<T>()` helper handles both formats and always throws `CascadeIpcError`.

Common error codes:

| Code | Meaning |
|---|---|
| `DaemonNotRunning` | The `cascaded` daemon is not reachable on its socket |
| `ResourceNotFound` | A requested file or key does not exist |
| `PermissionDenied` | The operation is not allowed for the current context |
| `NotImplemented` | The command handler is a stub pending a future phase |
| `UnknownError` | A non-string rejection that could not be classified |

---

## Command Reference

| Command Name | Tauri Handler | TypeScript Method | Parameters | Return Type | Notes |
|---|---|---|---|---|---|
| `cascade_status` | `cascade_status` | `ipc.getStatus()` | none | `StatusResult` | |
| `cascade_resolve` | `cascade_resolve` | `ipc.resolve(tier?, format?)` | `tier?` string, `format?` string | `ResolveResult` | |
| `cascade_search` | `cascade_search` | `ipc.search(query, limit?)` | `query` string (req), `limit?` number | `SearchResult` | |
| `cascade_inbox_list` | `cascade_inbox_list` | `ipc.inbox.list(limit?)` | `limit?` number | `InboxSummaryResult` | |
| `cascade_inbox_send` | `cascade_inbox_send` | `ipc.inbox.send(to, subject, body, priority?)` | `to`, `subject`, `body` (all req), `priority?` string | `InboxSendAck` | NOT IMPLEMENTED — stub until T-P4-E01 |
| `cascade_memory_read` | `cascade_memory_read` | `ipc.memory.read(project, file)` | `project`, `file` (both req) | `MemoryReadResult` | |
| `cascade_memory_write` | `cascade_memory_write` | `ipc.memory.write(project, file, content)` | `project`, `file`, `content` (all req) | `MemoryWriteResult` | |
| `cascade_config_get` | `cascade_config_get` | `ipc.config.get(key)` | `key` string (req) | `ConfigGetResult` | |
| `cascade_config_set` | `cascade_config_set` | `ipc.config.set(key, value)` | `key` string (req), `value` unknown (req) | `ConfigSetResult` | |

---

## Per-Command Usage

### `cascade_status` — Daemon Status

Returns the daemon's runtime state. Call this on mount to check whether `cascaded` is running before attempting other commands.

```typescript
import { ipc } from '@/lib/ipc/client'
import { CascadeIpcError } from '@/types/errors'

try {
  const status = await ipc.getStatus()
  console.log(`daemon PID: ${status.pid}, uptime: ${status.uptime_secs}s`)
  console.log(`queue depth: ${status.queue_depth}, RAG fresh: ${status.rag_index_fresh}`)
} catch (err) {
  if (err instanceof CascadeIpcError && err.code === 'DaemonNotRunning') {
    // Show a "start the daemon" prompt
  }
}
```

`StatusResult` shape: `{ pid: number, uptime_secs: number, queue_depth: number, rag_index_fresh: boolean, version: string, tcp_port?: number }`.

---

### `cascade_resolve` — Resolve Instruction Cascade

Returns the resolved cascade instructions for a given tier, or the full cascade when `tier` is omitted.

```typescript
// Full cascade, markdown format (default)
const full = await ipc.resolve()

// Just the GCI tier, as JSON
const gciJson = await ipc.resolve('gci', 'json')

// Valid tier strings: "gci" | "pci" | "apc" | "ppc" | "prc" | "pac"
```

`ResolveResult` shape: `{ content: string, format: string, tier: string }`.

---

### `cascade_search` — Knowledge Base Search

Searches the indexed knowledge base (RAG index). Returns ranked hits using BM25 FTS5 + vector search + RRF merge.

```typescript
const results = await ipc.search('authentication flow', 10)
for (const hit of results.hits) {
  console.log(`[${hit.score.toFixed(3)}] ${hit.path}: ${hit.snippet}`)
}
```

`SearchResult` shape: `{ hits: Array<{ path: string, score: number, snippet: string }> }`. Returns empty `hits` when the index is not yet built.

---

### `cascade_inbox_list` — List Inbox Messages

Lists messages in the active cascade context's inbox directory.

```typescript
const inbox = await ipc.inbox.list(20)
for (const item of inbox.items) {
  console.log(`[${item.priority}] ${item.subject} — ${item.from}`)
}
```

`InboxSummaryResult` shape: `{ items: Array<{ id: string, subject: string, from: string, priority: string, created: string, path: string }> }`. Items are in reverse-chronological order.

---

### `cascade_inbox_send` — Send Inbox Message

**NOT IMPLEMENTED.** This command is a stub returning `NotImplemented` until T-P4-E01 adds the full daemon handler. Do not use this in production code during P3.

```typescript
// This throws CascadeIpcError("NotImplemented") until T-P4-E01
try {
  const ack = await ipc.inbox.send('nself', 'Re: gap', 'Details here', 'medium')
} catch (err) {
  if (err instanceof CascadeIpcError && err.code === 'NotImplemented') {
    // Expected — stub until P4
  }
}
```

`InboxSendAck` shape (future): `{ id: string, path: string }`.

---

### `cascade_memory_read` — Read Memory File

Reads a file from a project's `.claude/memory/` directory.

```typescript
// project can be an absolute path or a slug registered with the daemon
const result = await ipc.memory.read('/Volumes/X9/Sites/nself', 'decisions.md')
console.log(result.content) // full file text
console.log(result.path)    // absolute path to the file
```

`MemoryReadResult` shape: `{ content: string, path: string }`. Throws `ResourceNotFound` if the file does not exist.

---

### `cascade_memory_write` — Write Memory File

Writes (overwrites) a file in a project's `.claude/memory/` directory. Creates the file if it does not exist.

```typescript
const updated = `# Decisions\n\n## 2026-06-06\nChose BGE-M3 for embeddings.\n`
const result = await ipc.memory.write('/Volumes/X9/Sites/nself', 'decisions.md', updated)
console.log(`wrote ${result.bytes} bytes to ${result.path}`)
```

`MemoryWriteResult` shape: `{ path: string, bytes: number }`. This command always overwrites; there is no append mode.

---

### `cascade_config_get` — Get Config Key

Reads a configuration value from the daemon's runtime config store.

```typescript
const result = await ipc.config.get('daemon.socket_path')
console.log(result.key)   // "daemon.socket_path"
console.log(result.value) // e.g. "/tmp/cascade.sock"
```

`ConfigGetResult` shape: `{ key: string, value: unknown }`. The `value` type depends on the key. Throws `ResourceNotFound` for unregistered keys.

---

### `cascade_config_set` — Set Config Key

Updates a configuration value in the daemon's runtime config store.

```typescript
const result = await ipc.config.set('ui.theme', 'dark')
console.log(result.key)       // "ui.theme"
console.log(result.previous)  // prior value, or undefined if key was new
```

`ConfigSetResult` shape: `{ key: string, previous?: unknown }`. Throws on invalid key or type mismatch.
