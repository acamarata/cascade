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

## The MCP tools a session sees

This plugin registers the Cascade MCP **server**; it does not define the
tools. The tools are core, declared in `internal/mcp/coretools` and served
by the binary this plugin writes into the harness's MCP config
(`cascade mcp serve --stdio`). A harness talks to the core, not to a plugin
(`R-14.251`).

### The v1-parity set

Eight of Cascade v1's twenty-four MCP tools have a v2 surface and are
registered:

| Tool | Answers through | Capability | Mutating |
|---|---|---|---|
| `cascade_context_search` (alias `cascade_search`) | `recall.query` | `context.read` | no |
| `cascade_context_show` | `context.show` | `context.read` | no |
| `cascade_context_slice` | `context.slice` | `context.read` | no |
| `cascade_context_sync` | `context.sync` | `context.read` | yes |
| `cascade_memory_recall` | `memory.recall` | `memory.read` | no |
| `cascade_memory_search` | `memory.recall` | `memory.read` | no |
| `cascade_memory_remember` | `memory.remember` | `memory.write` | yes |
| `cascade_memory_forget` | `memory.forget` | `memory.write` | yes |

The other sixteen are recorded in `internal/mcp/coretools/deferrals.go`,
each with what is actually missing and one of three outcomes (`R-14.266`):

- **Owned by a ticket.** `cascade.search_codebase` waits on the symbol
  graph AG/S-67.T3 registers as a retrieval corpus; the three inbox tools
  wait on AK/S-73.T3.
- **Served by a plugin.** The six PBD phase-tree tools are answered by
  `cascade-pbd`'s own `cascade_plugin_pbd_status` and
  `cascade_plugin_pbd_board`. Core cannot register them — `internal/**`
  may not import `plugins/**` — and it does not need to: the plugin's
  manifest surfaces them itself. Its mutating `plugin.pbd.claim/step/done`
  RPC is deliberately not a tool, so a model can read the board and not
  move a ticket on it.
- **Retired.** Master lists, the route check, the two memory FILE surfaces
  and the two security tools do not return. The memory pair is covered in
  concept by the four registered record-addressed tools. The secret
  scanner is refused on its merits: a tool that scans a repository for
  credentials and hands them to a model is an exfiltration surface with a
  helpful name.

The shape of the gap: v1's MCP surface was largely a filesystem API over a
project's `.claude` tree — tier files, master lists, a PCI inbox directory,
a PBD phase tree of YAML — and v2 does not have that tree.

`internal/mcp/coretools/testdata/v1-goldens/tools.json` is the harvested v1
inventory, and a test asserts the equation that makes it mean something:
every v1 tool is either registered or deferred with a ticket. A tool that
was dropped silently fails it, and a deferral marked retired or
served-by-plugin must name its ruling or its replacement surface — a
sentinel is otherwise a way to stop a row failing without deciding
anything.

### Which tools appear under which grants

A tool appears in `tools/list` only when the policy engine grants its
capability. `cascade policy grant context.read` makes the three context
tools appear on the next registration pass; revoking it makes them
disappear on the next one. Three rules govern it:

- **Fail-closed.** No engine, an unregistered capability, an evaluation
  error, or any verdict short of `allow` all mean the tool is absent. An
  `ask` verdict is a denial *here* even though it is not one at call time:
  a tool the user has not already granted must not appear in a list the
  model reads as "things I may call".
- **A denied tool is indistinguishable from an absent one.** The server
  reports both as unknown, so a model cannot learn that a privileged tool
  exists on this machine. `cascade mcp tools list` tells the *operator* the
  difference — `withheld` (grant it), `unservable` (this build serves no
  such method), `deferred` (no v2 surface yet, with the ticket).
- **Grants take effect on the next registration pass.** The filter is
  consulted once per registry. Restart the MCP server, or run
  `cascade context harness sync`, and the list is rebuilt.

On Windows the grant store composition does not exist yet, so no
capability-gated tool is listed there. That is an asserted refusal with a
test on it (Art.5), not a silent gap.

### MCP profiles

`R-21.179` splits the tool set into three named profiles, selected per
harness session by `[mcp].profile = compact | compact-write | full`:

- **`compact`** is the default registration and is READ-ONLY. A prompt
  injection reaching the default profile must not reach outbound messaging
  or quota spend in one step.
- **`compact-write`** is opt-in and holds the mutating tools. Cascade-
  launched executive sessions receive it; a session the user starts does
  not.
- **`full`** is the complete policy-filtered set described above, opted
  into with `cascade context harness sync --mcp-profile full`.

The profile-switch mechanism and each compact profile's membership belong
to `AK/S-73.T3` and `AP/S-82.T1` (`R-21.258`), not to this build. What is
true here already: **profile membership is never authorization.** A tool in
`compact-write` still passes the capability filter, so an ungranted
capability withholds it exactly as it would in `full`.

## Prompt hydration

Every prompt a harness session submits fires a `UserPromptSubmit` hook that
runs `cascade context slice --hook`. The hook reads the harness's payload on
stdin, assembles a budgeted context slice for that payload's working
directory, drops every retrieved record below the configured fused-score
floor, and injects what is left as additional context — so a session starts
knowing what Cascade knows, without anyone running a command.

The capsule's first line names what it is:

```
Cascade context (scope: repository/cascade, 3 items)
```

### It never blocks a prompt

Hydration is CONTEXT, not policy. There is no failure mode in the hook that
stops a prompt: a payload it cannot decode, a daemon it cannot reach, an
unbuilt retrieval index, its own three-second timeout — every one of them
prints nothing and exits 0, and the session proceeds unhydrated. It cannot
gate, block or modify a prompt, and it has no code path that tries.

The one thing it will not print is an EMPTY capsule. "Nothing cleared the
score floor" and "here is no context" are different statements, and only the
first one is true.

### That silence is the problem, so it is counted

Because it fails open, a degraded hydration is invisible from the user's
side: a session with no context worth injecting and a session whose slice
failed look identical. So every failure publishes a `context.hydration.degraded`
event, and `cascade doctor` reports the count over the trailing 24 hours —
warning above zero, failing above twenty. That check is the only place the
silence becomes visible.

The event goes through the daemon when one is reachable and straight onto
the event log otherwise. That split is not a preference: the store takes an
exclusive lock, so a hook that wrote directly while the daemon held the
database would fail in exactly the common case.

### Settings

`[context.hydration]` in `config.toml` — `enabled` (default true),
`budget_tokens` (2000), `min_score` (0.35), `timeout_seconds` (3). See
`docs/config-reference.md` § `[context.hydration]` keys.

`enabled` gates INSTALLATION: false means the hook descriptor is never
written into the harness's hook config, so nothing runs. It is not a privacy
control — scope safety is the scope resolver's job, and turning hydration off
does not make anything safer that was unsafe with it on.

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
