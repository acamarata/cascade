# cascade-claude

The builtin harness plugin. It makes an installed harness aware of Cascade:
instructions on disk, session hooks posting to the daemon, and the Cascade
MCP server registered so harness sessions can call its tools.

Runtime `builtin`, default on. Registered at compile time through
`pkg/plugin.RegisterBuiltin`; wired to its real collaborators by
`internal/plugins/claude_wiring.go`.

## Capabilities

| Capability | Entry point | What it does |
|---|---|---|
| Instruction install | `Install` | Writes the harness instruction golden the Context Engine renders, atomically, skipping files whose content already matches. |
| Hook pack install | `InstallHookPack` | Renders the registered hook packs for the daemon socket and installs the union config, with a version companion. |
| MCP registration | `RegisterMCP` | Adds the Cascade MCP server to the harness MCP config, preserving every entry and key it does not own. |

## What is deliberately not here

The ticket contract names two further capabilities. Neither is unfinished
work: each is blocked on a counterpart that does not exist, and shipping
either would have added a surface no running program could reach.

- **Session watch.** Republishing harness session transitions onto the
  daemon event bus needs a *client* of the fleet sessions SSE stream.
  `internal/fleet/sessions` ships the server half and no subscriber, and the
  ticket that built the stream closed having built only that half.
- **Uninstall hook.** Removing what install placed must record an audit
  entry. `internal/audit.Kind` is a closed, ratified set (R-21.235) with no
  plugin-lifecycle kind, and `Append` refuses an unknown kind outright —
  that package's own doc says a consumer needing another kind amends the
  contract rather than minting one at a call site. Extending a ratified
  taxonomy is a ruling, not a call-site decision.

Both are recorded with their exact blockers in this ticket's journal. The
uninstall half lands as one unit once the audit-kind question is settled,
so that removal, clean-state assertion and audit ship together.

## There is no `cascade harness install` command

Per R-14.51 this plugin exposes no `harness install` / `harness uninstall`
CLI verbs. Install fires from `cascade init` step 6 and from
`cascade context harness sync`. None of it is elevated.

## Non-interactive use

The install flow has no prompts. Under `CASCADE_NO_INPUT=1` or
`cascade init --yes` every step runs unattended, because no step needs
input or elevation.

## Platform paths

Resolved by an injectable resolver, never by probing the filesystem. An
unset variable is an error naming that variable rather than a fallback to
whatever path happens to exist.

| | ConfigRoot |
|---|---|
| darwin | `$HOME/Library/Application Support/Claude` |
| linux (and other XDG platforms) | `${XDG_CONFIG_HOME:-$HOME/.config}/claude` |
| windows | `%APPDATA%\Claude` — tier-2, refusal path only |

`MCPConfig` is `<ConfigRoot>/mcp.json`; `HookConfig` is `<ConfigRoot>/hooks/`.

Windows is tier-2 (Art.5): the path branch is real and tested, but every
host-facing entry point refuses there with a message naming the tier. The
refusal lives in one gate (`hostPathsFor`) so the capabilities themselves
stay platform-independent and directly testable on any host.

## Declared permissions

`read-context` (render the instruction golden) · `hook-emit` (session
lifecycle events to the daemon) · `mcp-register` (register the MCP server).

`testdata/README.md` records which external contracts are golden-verified
and which are not.
