# Troubleshooting

This page covers common problems and how to fix them. Run `cascade doctor` first - it catches most issues automatically.

```bash
cascade doctor
cascade doctor --fix    # attempt auto-repair
```

---

## Daemon not running

**Symptom:** `cascade status` shows "daemon: stopped" or commands hang with no output.

**Fix:**

```bash
cascade daemon start
cascade daemon status
```

If the daemon fails to start, check the log:

```bash
# macOS
tail -f ~/Library/Logs/cascade-daemon.log

# Linux
tail -f ~/.local/share/cascade/daemon.log
```

Look for a `FATAL` or `ERROR` line. Common causes:

- **Port already in use:** another process holds the socket path. Delete the stale socket: `rm ~/.cascade/cascade.sock`, then `cascade daemon start`.
- **Permission denied on socket directory:** check that `~/.cascade/` is writable by your user.
- **Binary missing from PATH:** if you installed via Cargo, make sure `~/.cargo/bin` is in your PATH.

---

## cascade search returns no results

**Symptom:** `cascade search "my query"` returns nothing, even though you have `.cascade/` files.

**Checks:**

1. Confirm indexing completed: `cascade status` should show `index: N documents` with N > 0.
2. Confirm RAG is enabled: `cascade config get rag.enabled` should print `true`.
3. Restart the daemon to trigger a reindex: `cascade daemon restart`.
4. Check that your `.cascade/` directory actually contains Markdown files: `ls ~/.cascade/`.

---

## Tool config not updating

**Symptom:** Changes to your `CASCADE.md` are not reflected in `CLAUDE.md` or `.cursorrules`.

**Checks:**

1. Confirm the tool integration is enabled: `cascade config get tools.claude_code`.
2. Confirm the file watcher is running: `cascade status` shows `watch: active`.
3. Force a regeneration: `cascade generate-instructions`.
4. If using a symlink, confirm it points to the right place: `ls -la CLAUDE.md`.

---

## Gatekeeper blocks the app (macOS)

**Symptom:** macOS shows "Cascade.app cannot be opened because it is from an unidentified developer."

This should not happen with a properly signed release. If you see this:

1. Check you downloaded from the official release: `https://github.com/acamarata/cascade/releases`.
2. Verify the signature: `codesign --verify --deep --strict Cascade.app`.
3. If the signature is invalid, the download may be corrupted. Re-download.
4. If you built from source, the binary is unsigned. Run: `xattr -dr com.apple.quarantine cascade` to bypass Gatekeeper for a locally-built binary.

---

## cascade migrate fails or skips files

**Symptom:** Running `cascade migrate ~/.claude` does not move all files, or exits with an error.

**Checks:**

1. Run with `--dry-run` first to see what would happen: `cascade migrate --dry-run ~/.claude`.
2. Check that the source directory exists and is readable.
3. If files are skipped, they may already exist in the target. Use `--force` to overwrite.

---

## MCP connection refused

**Symptom:** Your AI tool cannot connect to the Cascade MCP server.

**Checks:**

1. Confirm the daemon is running: `cascade daemon status`.
2. Confirm MCP is enabled: `cascade config get mcp.enabled`.
3. Confirm the address: `cascade config get mcp.bind` (default `127.0.0.1:9762`).
4. Test the endpoint: `curl http://127.0.0.1:9762/health` - should return `{"status":"ok"}`.
5. Check that the token in your client config matches a valid token: `cascade mcp token list`.

---

## High memory usage

**Symptom:** The daemon uses more than 1 GB of RAM.

The Multilingual E5 Large ONNX model has a substantial resident-memory cost when loaded. This is expected.

To reduce memory usage, disable dense embeddings:

```bash
cascade config set rag.dense_weight 0.0
cascade config set rag.fts_weight 1.0
cascade daemon restart
```

This falls back to FTS5-only search, which uses ~5–20 MB.

---

## Slow first search after daemon start

The ONNX model loads on the first search query. Subsequent queries are fast. This is a one-time warm-up cost.

---

## Logs and diagnostics

| Log location | Purpose |
|---|---|
| `~/Library/Logs/cascade-daemon.log` (macOS) | Daemon logs |
| `~/.local/share/cascade/daemon.log` (Linux) | Daemon logs |
| `cascade status` | Health summary |
| `cascade doctor` | Full diagnostic |
| `RUST_LOG=debug cascade daemon start` | Verbose daemon start |

---

## Getting help

If the steps above do not resolve the issue, open a [GitHub issue](https://github.com/acamarata/cascade/issues/new?template=bug_report.md). Include output from:

```bash
cascade --version
cascade doctor
cascade status --json
```

See also: [FAQ](FAQ.md) · [Security](Security.md)
