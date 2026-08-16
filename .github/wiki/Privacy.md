# Privacy

Cascade is designed for local-first operation. This page explains what data stays on your machine, what optional data may leave it, and how to control both.

---

## What stays local (always)

- All `.cascade/` instruction files and `CASCADE.md` sources
- The RAG index (SQLite database in `~/.cascade/`)
- The Multilingual E5 Large ONNX model (stored in `~/.cascade/models/`)
- All search queries and results
- Memory files, inbox messages, and snapshots
- Daemon logs
- API keys and tokens (stored in the OS keychain, never written to disk in plaintext)

None of this leaves your machine under any circumstances.

---

## What can leave your machine (opt-in only)

### Telemetry

Cascade does not collect telemetry by default. During the onboarding wizard, you are asked whether to send anonymous crash reports and usage counts. If you decline (or skip), no data is sent. See [Telemetry Consent](Telemetry-Consent.md) for what the optional telemetry contains.

### LLM provider API calls

If you configure an LLM provider (Claude, Gemini, OpenAI, or a local model), queries you make through the Cascade Gemini proxy or dispatch commands are sent to that provider. Cascade passes through your API key from the OS keychain. The provider's privacy policy governs what they do with query data. Cascade does not proxy or log LLM traffic beyond what the daemon's own log level captures.

### Plugin network access

WASM plugins declare their capabilities in `plugin.json`. A plugin with `network` capability can make outbound HTTP calls. Before installing any plugin, inspect its manifest with `cascade plugin inspect <NAME>` to see what capabilities it requests. Only install plugins you trust.

### Update checks

`cascade update check` queries the GitHub releases API to compare the installed version with the latest. This is an explicit user action. Auto-update (`cascade update auto --enable`) checks on the same schedule as the auto-update interval. The check sends only the current version number in the `User-Agent` header; no personal data is included.

---

## Data Cascade does not collect

- No AI conversation content
- No file contents from your repos (only the `.cascade/` instruction files are indexed)
- No system information beyond what your OS exposes to a normal user process
- No usage analytics without explicit opt-in

---

## Audit what data is on your machine

```bash
# Show all cascade directories on this machine
cascade status --show-tiers

# Show the index size
cascade cache stats

# Show stored tokens
cascade mcp token list
```

---

## Deleting your data

Remove the local index and config:

```bash
cascade uninstall --purge
```

This removes `~/.cascade/` including the index database, model cache, snapshots, and config. It does not remove per-repo `.cascade/` directories committed to repos.

Remove a specific token from the keychain:

```bash
cascade mcp token revoke <TOKEN_ID>
```

---

See also: [Telemetry Consent](Telemetry-Consent.md) · [Security](Security.md) · [FAQ](FAQ.md)
