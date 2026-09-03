# Plugin author guide

Status: seeded from Wave 1 (P1-E03-W1-S05-T7, the Epic C acceptance
ticket). This file currently documents the ONE pattern T7 establishes:
compile-time builtin registration. Ticket contract:
`.claude/planning/p1/phase/epics/E-C/waves/W-1/sprints/S-05/tickets/T-7.yaml`;
spec: `02-TARGET-STRUCTURE.md` §"First-party plugin catalog v1" and
`10-ROUND2-DELTAS.md` §D-21. Process (`RuntimeProcess`), wazero
(`RuntimeWasm`), and remote (`RuntimeRemote`) runtime modes are out of
this ticket's scope (O/S-31) and are not documented here yet.

## Compile-time builtin registration

A **builtin** plugin (`Manifest.Runtime == plugin.RuntimeBuiltin`) is Go
code linked directly into the `cascade` binary. It never runs as a
separate process, so it has no wire protocol to implement; instead it
registers itself with the host at compile time, via `init()`.

### The pattern

1. Implement `plugin.BuiltinHandlers`: the three-method dispatch
   surface the host calls once your plugin is loaded:

   ```go
   type BuiltinHandlers interface {
       DispatchTool(ctx context.Context, name string, input []byte) ([]byte, error)
       DispatchIntent(ctx context.Context, name string, input []byte) ([]byte, error)
       RunCommand(ctx context.Context, name string, args []string) error
   }
   ```

   Every method is ctx-first (02-TARGET-STRUCTURE.md §v1.1). Each
   `name` argument matches one of your manifest's declared
   `provides.tools[].name` / `provides.intents[].name` /
   `provides.commands[].name` entries. Return an error for any
   unrecognized name rather than a zero value, so a caller can tell
   "not implemented yet" from "succeeded with nothing to report".

2. Call `plugin.RegisterBuiltin(manifest, handlers)` from your
   package's own `init()`:

   ```go
   func init() {
       plugin.RegisterBuiltin(manifest(), handlers{})
   }
   ```

   `RegisterBuiltin` is safe to call concurrently and is a pure
   append: it performs NO validation itself.
   `internal/plugins.BuiltinRegistry.Load` is what runs `plugin.Validate`
   against every registered manifest and rejects invalid ones; an
   author who wants to check a manifest before shipping runs
   `plugin.Validate` directly against it in a test.

3. The host's composition root (`cmd/cascade`, D/S-06.T1) blank-imports
   your package to trigger the `init()` above:

   ```go
   import _ "github.com/acamarata/cascade/plugins/examples/example-builtin"
   ```

   A blank import is the entire integration surface for a builtin
   plugin: there is no manifest file on disk to install, no process to
   supervise, no manifest hash to verify at load time (that machinery
   exists for the process/wasm/remote runtime modes, not builtin).

See `plugins/examples/example-builtin/plugin.go` for a complete, real
(non-stub) worked example: one tool, one intent, one command, all
sharing the "greet" verb.

## The Grants model (W1)

`RegisterBuiltin` always seeds a registered plugin's `Grants` to
`[]string{"read"}`. This is the ONLY grant a builtin plugin receives
automatically in W1: there is no consent dialog, no capability request
flow, and no call into the capability-policy engine (I/S-17.T1, not yet
built). A plugin that needs more than read access declares it via
`Manifest.Requires` and `Manifest.Permissions` as normal; the
policy-engine ticket is what will actually enforce those declarations.
Until then, `internal/plugins.BuiltinRegistry.Grants(id)` is a read-only
mirror of what `RegisterBuiltin` seeded: it grants nothing beyond
`"read"` on its own.

## RPC naming convention

A plugin's `provides.commands[]` entries mount under a JSON-RPC method
name of the form:

```
plugin.<pluginID>.<commandName>
```

for example `plugin.example-builtin.greet`. `10-ROUND2-DELTAS.md` §D-21
is the normative source for this pattern.
`internal/plugins.BuiltinRegistry.RPCMethodName(pluginID, commandName)`
computes the string; `internal/plugins.BuiltinRegistry.NewCobraCommand`
builds the matching `*cobra.Command` descriptor, whose `RunE` dispatches
straight into your `BuiltinHandlers.RunCommand`. T7 defines the naming
and the descriptor construction only. The JSON-RPC method dispatch that
actually routes an incoming `plugin.<id>.<cmd>` call to your handler is
D/S-06.T3's, not yet built.

## Reserved names (read before choosing an id or a command name)

- A plugin `id` must match `[a-z][a-z0-9-]*` and must not claim the
  `plugin.__host__.*` namespace reserved for host-owned storage slots
  (R-14.100, R-14.127).
- A `provides.commands[].name` must not collide with a reserved core
  noun or utility verb (`config`, `daemon`, `provider`, `plugin`,
  `node`, `sync`, `backup`, `vault`, `chat`, `recall`, `memory`,
  `context`, `fleet`, `mcp`, `init`, `run`, `status`, `doctor`,
  `migrate`, `self-update`, `uninstall`, `version`, `completion`); see
  `pkg/plugin/validate.go`'s `reservedCommandNames` for the
  authoritative, current list.

Both are enforced by `plugin.Validate`, which
`internal/plugins.BuiltinRegistry.Load` runs against every registered
manifest before indexing it.
