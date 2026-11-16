# Hook rehydration

A hook's `action_params` may name vault secrets with tags such as
`<apikey>NAME</apikey>`. The daemon swaps a tag for the vault value only at
the last moment, inside the dispatch of one `plugin-call` action. This page
states what that guarantees and where it stops.

## What is guaranteed

- **Config holds tags, never values.** `config.toml`, `hooks.list`,
  `hooks.fires`, stored events and the daemon log carry the tagged form or
  a hash, never a vault value.
- **A binding must be accepted.** Each tag is a binding of vault name to
  hook and plugin. A new binding is a loosening: it needs
  `config.accept_loosening` over the exact file. An unaccepted binding on
  disk is refused at dispatch (result `rehydrate`), with no plugin call and
  no vault read.
- **The admitted config decides, not the file.** The seam reads bindings
  from `Reloader.Current`. A tag the egress pass added to a raw value has no
  binding and refuses the whole action with `secrets: tag not permitted at
  this boundary`.
- **Missing vault name refuses.** An accepted binding whose name is absent
  from the vault refuses the action with no partial params.
- **One caller.** `cmd/cascade/hooks_rehydrate.go` is the only production
  file that calls `RehydrateOnly`; an architecture test enforces it.
- **Hooks run as a tool.** Runner contexts carry `rpc.OriginTool` and no
  elevation witness, so a hook cannot call a verified-elevation verb.
- **Retries do not repeat the call.** One delivery is one effect record; a
  redelivered or post-crash event never runs the plugin twice.

## Limits

| Limit | Effect |
|---|---|
| Plugin stderr echo | A trusted plugin that fails by echoing its input puts that text in its own error. The daemon redacts the vault value, its tagged form and the input hash from `hooks.list`, `hooks.fires`, events and the log. It cannot redact text a plugin writes to its own stderr stream or to files and sockets it controls. A plugin you trust with a secret can leak it. |
| Go string copies | `zero()` wipes the rehydrated buffer. The param strings decoded from it are Go copies that zeroing cannot reach. |
| JSON syntax in a value | A vault value holding a quote or backslash does not decode back to the tagged params. The action is refused rather than delivered changed. |
| `pii` tags | A `pii` tag in a param fails the dispatch scan and the action is refused. |
| Long names | A binding whose name is long enough to read as high-entropy may be refused by the egress pass before the seam runs. |

See `docs/developer/hooks.md` for the composition and the dispatch-time
guard.
