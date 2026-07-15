# Quickstart

Get a project-level Cascade setup working in about five minutes.

---

## Step 1: Install

Run the installer for your platform. See [Installation](Installation.md) for package-manager and source-build options.

**macOS / Linux:**

```sh
curl -fsSL https://raw.githubusercontent.com/acamarata/cascade/main/scripts/install.sh | sh
```

**Windows:**

```powershell
irm https://raw.githubusercontent.com/acamarata/cascade/main/scripts/install.ps1 | iex
```

The scripts install both `cascade` and `cascaded`, register the user-scoped daemon service, run `cascade init --accept-defaults` in the current directory, and run `cascade verify`. Daemon registration or initialization can be skipped with installer environment variables, but verification always runs and reports failures as warnings.

Confirm the CLI is on PATH:

```sh
cascade --version
```

On Windows, open a new terminal first if the installer added the PATH entry.

---

## Step 2: Initialize the project

Move to the project and explicitly select the canonical `.cascade` folder:

```sh
cd ~/my-project
cascade init . --accept-defaults --folder .cascade
```

The positional path defaults to the current directory. `--accept-defaults` makes reruns safe: it heals missing standard directories and leaves existing files alone. Initialization creates `.cascade/CASCADE.md`, standard working subdirectories, built-in skill and agent files, a local `.gitignore`, and `CLAUDE.md`/`AGENTS.md` sibling links inside `.cascade/`.

---

## Step 3: Add a rule

Edit `.cascade/CASCADE.md`. It is plain Markdown; for example:

```markdown
# My project context

Always write tests before implementation.
```

At a Git repository root, `cascade init` labels this as PRC (Per-Repo Cascade) context.

---

## Step 4: Connect a provider

The verification gate requires either a connected cloud provider or an installed local model. To connect a cloud provider:

```sh
cascade provider add --kind anthropic --api-key YOUR_API_KEY
```

Supported provider kinds are `anthropic`, `openai`, `gemini`, `openrouter`, `groq`, `mistral`, `deepseek`, `together`, and `cohere`. The key is validated and stored in the OS keychain rather than a plaintext project file.

---

## Step 5: Generate harness files

Preview the changes, then generate for Claude Code and OpenCode:

```sh
cascade generate-instructions --dry-run
cascade generate-instructions
```

For the project tier, the default `both` mode manages:

- `.claude/CLAUDE.md`, `.claude/AGENTS.md`, and `.claude/settings.json` for Claude Code.
- `.cascade/opencode-instructions.md` and `opencode.json` for OpenCode, plus the Cascade entry in `~/.config/opencode/opencode.json`.

Generation preserves unrelated JSON settings. An instruction file that already has the Cascade marker is skipped on rerun rather than refreshed; use `--dry-run` to see which targets will change. Use `--harness cc` or `--harness oc` to target only one harness.

---

## Step 6: Verify

```sh
cascade verify
```

The command checks:

- The active AI folder and readable `CASCADE.md`.
- A non-empty resolved cascade.
- The daemon socket (warning by default, failure with `--require-daemon`).
- A connected cloud provider or installed local model.
- Valid `config.toml` syntax.
- OS keychain access.

The exit code is zero when none of the six checks is `FAIL`; warnings are allowed. Run `cascade doctor` for the broader diagnostic report.

---

## Step 7: Resolve and search

Inspect the exact merged instructions first:

```sh
cascade resolve
```

The current CLI search ranks matching paragraphs from the resolved cascade by keyword occurrence:

```sh
cascade search "authentication"
cascade search "how should I handle errors" --top 5
cascade search "lessons" --scope memory
```

`--scope memory` searches Markdown files in the nearest `.cascade/memory/` directory. Use `--json` for structured search output.

---

## What to do next

- Add global rules in `~/.cascade/CASCADE.md`, or app-specific rules in an app's `.cascade/CASCADE.md`.
- Preview a single tier with `cascade generate-instructions --tier prc --dry-run`.
- Configure MCP clients with `cascade mcp setup --list`, then `cascade mcp setup --tool <TOOL>`.
- Create compatibility links with `cascade link --tool cursor` or `cascade link --tool aider`.
- Inspect installed plugins with `cascade plugin list`, or scaffold one with `cascade plugin new <NAME> --no-interactive`.
- Read [Cascade Concepts](Cascade-Concepts.md) for merge and generation semantics.

---

See also: [Home](Home.md) · [Installation](Installation.md) · [CLI Reference](CLI-Reference.md) · [Configuration](Configuration.md) · [Troubleshooting](Troubleshooting.md)
