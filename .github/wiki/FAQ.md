# FAQ

Answers to common questions about Cascade.

---

**Does Cascade replace my `.claude/CLAUDE.md` or other existing tool config files?**

No. Cascade generates tool-specific files alongside your existing ones, or can migrate legacy configs into the cascade hierarchy. It does not delete anything without your explicit confirmation (`cascade uninstall --purge` is the only command that removes `~/.cascade/`). After migration, your existing `.claude/CLAUDE.md` becomes the `CLAUDE.md` file Cascade generates from your cascade hierarchy.

---

**Is my data sent to any server?**

No. Cascade is fully local. The RAG index, embeddings, and instruction files live on your disk. The BGE-M3 model runs locally via ONNX. The MCP server only listens on `127.0.0.1` by default. There is no telemetry unless you explicitly opt in during onboarding. See [Privacy](Privacy.md) and [Telemetry Consent](Telemetry-Consent.md).

---

**How does code signing work?**

On macOS, the app and CLI binary are signed with an Apple Developer ID certificate and notarized. On Windows, binaries are Authenticode-signed. Linux releases include SHA256 checksums and a GPG signature. See [Code Signing](Code-Signing.md) for verification commands.

---

**Can I use Cascade with only the CLI, without the desktop app?**

Yes. The CLI (`cascade`) and daemon (`cascaded`) work entirely without the GUI. The Tauri desktop app is optional. All functionality available in the app is also available via `cascade` subcommands and the config file. The app is a convenience wrapper, not a requirement.

---

**What happens to my existing tool configurations after I link a tool?**

`cascade link --tool cursor` creates a symlink (`.cursorrules`) pointing at the generated cascade output. If a `.cursorrules` file already exists, Cascade moves it to a backup before creating the symlink. You can restore it with `cascade restore --tool cursor`. No data is discarded.

---

**How does the six-tier hierarchy work if I only use one project?**

You do not need all six tiers. The most common setup is two tiers: GCI (`~/.cascade/`) for global rules and PRC (per-repo `.cascade/`) for project-specific ones. Cascade merges whatever tiers exist and skips missing ones. You can start with just a global tier and add project-level tiers later.

---

**I use multiple AI tools on the same codebase. Do I maintain separate configs for each?**

That is exactly what Cascade solves. You maintain one `CASCADE.md` per scope. Cascade generates the tool-specific format for each tool you enable. Enable or disable tools:

```bash
cascade config set tools.cursor true
cascade config set tools.aider false
```

---

**Does Cascade work on Windows?**

Yes. The CLI and daemon work on Windows 10+. The GUI app targets Windows 11. Distribution is available via Winget, Chocolatey, Scoop, and direct download. The Windows widget requires Windows 11 (Windows App SDK 1.5). See [Install](Install.md) for platform-specific notes.

---

**Can multiple developers on a team share cascade configuration?**

Yes. Commit the `.cascade/` directory to your repo. Every team member who installs Cascade will have the same project-level instructions. Global tier (`~/.cascade/`) stays private per developer. The per-repo tier (committed `.cascade/`) is shared. This is the intended model: shared project conventions in the repo, personal preferences in the global tier.

---

**What happens if the daemon crashes?**

The CLI falls back to reading `.cascade/` files directly for most operations. Search (`cascade search`) requires the daemon for embeddings but falls back to FTS5-only if the daemon is unavailable. The daemon logs to `~/Library/Logs/cascade-daemon.log` (macOS) or `~/.local/share/cascade/daemon.log` (Linux). Run `cascade doctor` to diagnose and restart the daemon if needed.

---

See also: [Troubleshooting](Troubleshooting.md) · [Security](Security.md) · [Privacy](Privacy.md) · [Install](Install.md)
