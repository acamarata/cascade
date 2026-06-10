# Cascade Onboarding Wizard

The onboarding wizard runs on first launch and whenever you run `cascade init`. It connects your AI providers, scans for existing tool configs, migrates content into the Cascade tier structure, and installs the background daemon. Every action is reversible.

---

## Prerequisites

- macOS 13+, Linux (glibc 2.35+), or Windows 11
- At least one AI provider account: Gemini (free tier), Anthropic (Claude), OpenAI, or OpenCode Go. You can also download a local model during setup instead.
- Optional: existing `~/.claude/`, `~/.cursor/`, or other tool config directories (the scanner finds them automatically)

---

## Phases

The wizard has 10 phases. The left sidebar shows your position. You can navigate back to any completed phase at any time.

### 1. Welcome

A 30-second animated explainer of the six-tier cascade: GCI → PCI → APC → PPC → PRC → PAC. Each tier appears in sequence with a connecting line drawn downward. The final frame shows: "Every tool reads the same rules. Written once, understood by all."

- Press **Skip** to jump to the final frame immediately.
- If `prefers-reduced-motion` is enabled in your OS, all tiers appear at once with no animation.

### 2. Connect a Provider

Choose which AI providers to connect. Options:

| Provider | Notes |
|----------|-------|
| Gemini | Recommended for new users. Generous free tier. Auto-detected if accounts exist on disk. |
| Anthropic (Claude) | State-of-the-art reasoning. Requires an API key. |
| OpenAI | GPT-4 and o-series models. Requires an API key. |
| OpenCode Go | Open-source, self-hosted option. |
| Local model | Downloads Gemma 2 2B or Llama 3.2 3B via Ollama. No API key needed. |

You must connect at least one provider, or choose a local model, before continuing. Gemini accounts are detected automatically on mount.

### 3. Scan Legacy Configs

Cascade scans your machine for existing tool configuration directories. It looks for:

- Claude Code (`~/.claude/`)
- Codex
- Cursor
- Aider
- Windsurf

The scan runs automatically in two passes: a global home-directory scan and a dev-tree scan (you can point the dev-tree scan at any directory). Both passes are read-only. Nothing is moved or changed at this step.

Results appear in a table: tool name, detected paths, and file count. If nothing is found, you can continue to the next step.

### 4. AI Merge

Cascade reads your existing instruction files and uses an AI model to merge the content into the correct tier slots within `~/.cascade/`. This step handles conflicts: if two tools define the same rule, the merge presents both versions and lets you pick one or combine them.

Source files stay untouched at this step. The merge output is staged for review in the next phase.

### 5. Tool Modes

For each detected tool, choose how Cascade manages it:

| Mode | What it means |
|------|---------------|
| **Cascade-managed** | Cascade writes a symlink from the tool's config directory to `~/.cascade/`. The tool reads cascade-managed instructions automatically. |
| **Independent** | The tool keeps its own config directory. Cascade does not touch it. |

Use **Select all** / **Deselect all** to set all detected tools at once. Undetected tools (not found in the scan) are shown grayed out and excluded from bulk selection.

### 6. Verify Diff

Review every change before anything is written. The diff view shows:

- Content that will be added to each tier file
- Symlinks that will be created
- Files that will be archived

Nothing happens until you press **Apply**. You can go back to any earlier phase and adjust your choices.

### 7. Archive Legacy

Cascade moves the original config directories for any tools set to Cascade-managed into `~/.cascade/legacy/<tool-id>/`. The original paths are freed up for the symlinks created in the next phase.

Key facts:

- Files are **moved**, not deleted. Every byte is preserved.
- `~/.cascade/` itself is never archived.
- An archive manifest is written to `~/.cascade/legacy/manifest.json` listing every moved file with its original path. This manifest drives the restore command.
- Already-archived tools show an "Archived" badge and are skipped on subsequent runs.
- You must click **Archive selected tools** explicitly. Nothing archives on page load.

### 8. Symlink Setup

Cascade creates symlinks so each tool reads the cascade-managed tier files. A preview table shows the planned operations before anything runs:

| Kind | What happens |
|------|--------------|
| `replace` | The tool's original config path is replaced by a symlink pointing into `~/.cascade/`. |
| `sibling` | A symlink is created alongside the existing config (used when the tool supports multiple config sources). |

An empty plan (no Cascade-managed tools) shows "Nothing to link" and lets you skip to the next step. The symlinks are applied only after you click **Apply**.

### 9. Daemon Install

Cascade installs the `cascaded` background daemon, which handles context compression, RAG queries, and IPC between tools. The installer:

1. Checks your OS (macOS, Linux, or Windows).
2. Writes the daemon binary to the appropriate location.
3. Registers it with launchd (macOS) or systemd (Linux) so it starts at login.

Progress and errors appear in real time. If installation fails, the error message includes the exact command that failed so you can run it manually.

### 10. Done

Setup is complete. The Done screen shows:

- A health dashboard: provider status, daemon status, and which tools are now Cascade-managed.
- A **Start tutorial** button that opens an interactive overlay walking through the main app features.
- A **Go to dashboard** button that exits the wizard.

---

## Files Written During Setup

| File | Written by | Purpose |
|------|-----------|---------|
| `~/.cascade/wizard-state.json` | All phases | Checkpoint: current step, completed steps, scan results, merge results, archive manifest path, daemon status |
| `~/.cascade/legacy/<tool-id>/` | Archive phase | Moved original tool config directories |
| `~/.cascade/legacy/manifest.json` | Archive phase | Maps each archived file to its original path |
| `~/.cascade/{gci,pci,apc,ppc,prc,pac}/` | AI Merge phase | Tier instruction files merged from your existing configs |
| Symlinks at original tool paths | Symlink Setup phase | Point each tool's config location into `~/.cascade/` |

---

## Resume and Checkpoints

The wizard saves a checkpoint to `~/.cascade/wizard-state.json` after every step transition. If you close the app mid-wizard, it picks up where you left off on next launch. The checkpoint stores:

- Current step
- Completed steps
- Provider connection status
- Scan results
- Merge results
- Archive manifest path
- Daemon installation status

You can also navigate backward to any completed step using the sidebar or the **Back** button.

---

## Recovery and Undo

### Restore archived tool configs

To move a tool's original config back from `~/.cascade/legacy/` to its original location:

```
cascade restore <tool-id>
```

The manifest at `~/.cascade/legacy/manifest.json` records the original path for every file. The restore command moves the files back and removes the symlink.

To restore all archived tools at once:

```
cascade restore --all
```

### Change a tool's mode after setup

Open **Settings > Tools** in the Cascade app. You can switch any tool between Cascade-managed and Independent. Switching to Independent removes the symlink and restores the original config directory from the archive. Switching to Cascade-managed creates the symlink (archiving the config first if it still exists at the original path).

### Remove Cascade entirely

```
cascade uninstall
```

This stops the daemon, removes all symlinks, restores all archived configs to their original locations, and removes `~/.cascade/`. Your original tool config directories are left exactly as they were before setup.

### Re-run the wizard

```
cascade init
```

Runs the full wizard again. Existing tier files and archived configs are detected automatically. The scanner skips anything already managed by Cascade.

---

## Reversibility Summary

| Action | Reversible? | How |
|--------|-------------|-----|
| Provider connection | Yes | Disconnect in Settings > Providers |
| AI merge (tier files written) | Yes | Edit or delete the tier files in `~/.cascade/` |
| Tool mode set to Cascade-managed | Yes | Switch to Independent in Settings > Tools |
| Archive (original configs moved) | Yes | `cascade restore <tool-id>` |
| Symlinks created | Yes | Removed automatically on restore or uninstall |
| Daemon installed | Yes | `cascade uninstall` removes it |
| Full uninstall | Yes (all original files restored) | `cascade uninstall` |

---

## CLI Reference

| Command | Description |
|---------|-------------|
| `cascade init` | Re-run the onboarding wizard |
| `cascade restore <tool-id>` | Move a tool's config back from `~/.cascade/legacy/` to its original path |
| `cascade restore --all` | Restore all archived tool configs |
| `cascade uninstall` | Remove the daemon, symlinks, and `~/.cascade/`; restore all original configs |
| `cascade status` | Show which tools are Cascade-managed and daemon health |

---

## Troubleshooting

| Problem | Cause | Fix |
|---------|-------|-----|
| Scanner finds nothing | Tool configs are in a non-standard location, or no supported tools are installed | Use the "Browse" button in the ScanLegacy phase to point the dev-tree scan at the correct directory |
| Provider not detected (Gemini) | No Gemini account files found on disk, or detection timed out (>500ms) | Enter your API key manually on the ProviderConnect screen |
| AI merge fails | The AI model returned an error or the tier file structure could not be parsed | Check your provider connection in Settings > Providers, then go back to the Merge phase and retry |
| Archive fails with permission error | The original config directory is owned by root or another user | Run `sudo chown -R $USER ~/.claude/` (or the relevant tool path), then retry the archive |
| Restore conflict: file already exists | A file at the original path was created after the archive | The restore command will report the conflicting path; rename or delete the new file, then retry |
| Daemon fails to start after install | launchd/systemd registration failed | Check `~/Library/Logs/cascade-daemon.log` (macOS) or `journalctl -u cascaded` (Linux) for the error message |
| Wizard does not resume after restarting | `~/.cascade/wizard-state.json` is missing or corrupt | Delete the file and run `cascade init` to start fresh; your archived configs and tier files are not affected |

---

## Testing

The wizard has two complementary test layers:

### Integration tests (vitest + React Testing Library)

Run without a binary or display server:

```bash
pnpm --dir apps/cascade-app exec vitest run
```

The suite at `apps/cascade-app/e2e/wizard.integration.test.tsx` covers all 10 wizard phases at the React level. All Tauri commands are mocked via `vi.mock('@tauri-apps/api/core')`. The mock layer lives in `e2e/mocks/tauriMocks.ts`.

| Scenario | What is verified |
|---|---|
| Welcome renders + skip-animation | `WelcomePhase` renders, clicking skip fires `goNext` |
| Provider detect badge | Step 2 stepper label "Connect AI" appears |
| Scan triggers `scan_global_homes` | Invoke spy records the command on step 3 mount |
| Merge pipeline fires | `read_legacy_content` + `run_ai_merge` invoked at step 4 |
| Section approve/reject | Approve buttons found; rejected sections excluded from write payload |
| Tool modes reachable | Step 5 "Configure Tools" renders all 7 known tool cards |
| Archive `list_archived_tools` | Called on step 7 mount regardless of scan state |
| Checkpoint save + load | `wizard_save_checkpoint` persists; `loadCheckpoint()` returns saved state |
| Full flow — all 10 steps | `wizard_mark_complete` invoked after step 10 "Open Cascade" click |

### E2E tests (WebdriverIO + tauri-driver)

Require a compiled Tauri binary and a display server. Annotated `@ci-with-display`:

```bash
# Build first
pnpm --dir apps/cascade-app tauri:build

# Run (macOS native display, or Linux with Xvfb :99)
pnpm --dir apps/cascade-app test:e2e:wdio
```

Config: `apps/cascade-app/e2e/wdio.conf.ts`. Spec: `apps/cascade-app/e2e/wizard.e2e.ts`.

Add to CI with:

```yaml
e2e:
  runs-on: ubuntu-latest
  steps:
    - name: Start Xvfb
      run: Xvfb :99 -screen 0 1280x768x24 &
    - name: Build
      run: pnpm --dir apps/cascade-app tauri:build
    - name: Run E2E
      env:
        DISPLAY: ':99'
      run: pnpm --dir apps/cascade-app test:e2e:wdio
```

---

**Last Updated:** 2026-06-09
