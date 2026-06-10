# MCP Server

The Cascade daemon ships a built-in MCP (Model Context Protocol) server. It exposes your cascade hierarchy as context resources, search tools, and memory operations that any MCP-compatible tool can consume.

---

## What it provides

| Category | Items |
|---|---|
| Resources | Active cascade tiers, merged instruction text, memory files |
| Tools | `search_cascade`, `get_tier`, `write_memory`, `list_inbox` |
| Prompts | `cascade_context` (full merged context as a system prompt prefix) |

---

## Start the server

The MCP server starts automatically with the daemon:

```bash
cascade daemon start
```

To verify it is running:

```bash
cascade status
# look for: MCP server: running on 127.0.0.1:9762
```

To disable:

```bash
cascade config set mcp.enabled false
cascade daemon restart
```

---

## Transports

Cascade MCP supports five transports. Choose the one your client requires.

| Transport | Address | Notes |
|---|---|---|
| HTTP (SSE) | `http://127.0.0.1:9762/sse` | Standard MCP SSE transport |
| stdio | `cascade mcp stdio` | For tools that spawn subprocesses |
| WebSocket | `ws://127.0.0.1:9762/ws` | For browser-based or WS-native clients |
| Unix socket | `unix://~/.cascade/mcp.sock` | Fastest, macOS/Linux only |
| Named pipe | `\\.\pipe\cascade-mcp` | Windows only |

---

## Authentication

By default, the MCP server requires a bearer token. Generate one:

```bash
cascade mcp token generate
# → token: cas_abc123...
```

Pass the token in the `Authorization` header:

```
Authorization: Bearer cas_abc123...
```

To disable auth for local-only setups:

```bash
cascade config set mcp.auth none
cascade daemon restart
```

---

## Client setup

### Claude Code

Add to your `claude_desktop_config.json` (macOS: `~/Library/Application Support/Claude/claude_desktop_config.json`):

```json
{
  "mcpServers": {
    "cascade": {
      "command": "cascade",
      "args": ["mcp", "stdio"],
      "env": {}
    }
  }
}
```

Or connect via HTTP SSE with a token:

```json
{
  "mcpServers": {
    "cascade": {
      "url": "http://127.0.0.1:9762/sse",
      "headers": {
        "Authorization": "Bearer cas_abc123..."
      }
    }
  }
}
```

Run the automated setup to configure this for you:

```bash
cascade setup-oc   # for OpenCode
# For Claude Code, the config above applies manually
```

### OpenCode

```bash
cascade setup-oc
```

This writes the MCP server entry into OpenCode's project config and injects context for the current repo.

### Other clients

Any MCP-compatible tool can connect via the SSE or stdio transport. The server advertises its capabilities on connection. No custom client code is required.

---

## Available tools

### search_cascade

Semantic search over indexed cascade content.

```
Input: { "query": "authentication pattern", "top_k": 5 }
Output: { "results": [ { "text": "...", "source": "gci/CASCADE.md", "score": 0.94 } ] }
```

### get_tier

Fetch the raw content of a specific tier's `CASCADE.md`.

```
Input: { "tier": "prc" }
Output: { "tier": "prc", "path": "/home/user/project/.cascade/CASCADE.md", "content": "..." }
```

### write_memory

Write to a `.cascade/memory/` file at a specified tier.

```
Input: { "tier": "prc", "file": "decisions.md", "content": "ADR-001: use FTS5..." }
Output: { "written": true }
```

### list_inbox

List unread inbox messages.

```
Input: {}
Output: { "messages": [ { "id": "...", "subject": "...", "created_at": "..." } ] }
```

---

## Token management

```bash
cascade mcp token list                  # list active tokens
cascade mcp token generate              # create a new token
cascade mcp token revoke <TOKEN_ID>     # revoke a specific token
```

Tokens are stored in the OS keychain. They are never written to disk in plaintext.

---

## Troubleshooting

**Port already in use**

Change the bind address:

```bash
cascade config set mcp.bind "127.0.0.1:9763"
cascade daemon restart
```

**Connection refused**

Make sure the daemon is running:

```bash
cascade daemon status
cascade daemon start
```

**Auth failure (401)**

Regenerate your token:

```bash
cascade mcp token generate
```

Update the token in your client config. The old token continues working until revoked.

See also: [Troubleshooting](Troubleshooting.md) for general diagnostics.
