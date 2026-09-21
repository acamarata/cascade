# `cascade plugin search`

Searches the plugin registry catalog: the compiled-in builtin plugins this
binary always ships, plus — when a verified registry index is configured —
matching entries from that registry, alongside the builtins (never instead of
them).

```
cascade plugin search [<query>] [--json]
```

With `<query>`: entries whose name, description, or tags contain it,
case-insensitively. Without one: every catalog entry (browse mode).

## What it prints

```
$ cascade plugin search
NAME           VERSION  RUNTIME  DESCRIPTION
cascade-claude 1.0.0    go
cascade-pa     1.0.0    go
...
```

With `--json`, the same rows as a JSON array under `"data"`:

```json
{"ok":true,"data":[{"id":"cascade-pa","name":"cascade-pa","latest_version":"1.0.0","runtime":"go"}]}
```

A query with no match is not an error: exit 0, an empty table.

## Where the answer comes from

Unlike most `cascade` commands, `plugin search` does not branch on whether a
live daemon answered the startup socket probe. It branches on whether a
daemon is **configured** for this process at all:

- **A daemon is configured** (an explicit `[daemon].socket` entry in
  `config.toml`, or a socket file already on disk at the default location —
  either one means an operator set this environment up to run a daemon) and
  reachable: the command dials the daemon's `plugin.search` RPC method,
  which answers from a `*pkg/plugin.RegistryClient` built once, at daemon
  startup, from the `[registry]` config section.
- **A daemon is configured but down**: the command surfaces the real
  connection error and exits non-zero. It never falls back to the local
  catalog in this case — a configured daemon that will not answer is an
  operational problem, not "no daemon."
- **No daemon is configured at all**: the command runs the identical catalog
  decision locally, with no daemon and no network — this is the state a
  fresh install or an in-process script (no `config.toml`, no prior `cascade
  daemon start`) is in.
- **`cascade init`'s plugin catalog page** (step 4) renders through this same
  function, so the wizard and the CLI can never disagree about what is
  installable.

## The `[registry]` section

```toml
[registry]
url = "https://registry.example/index"
cache_dir = "..."      # defaults under the cascade data directory
cache_ttl = "15m"       # default
pubkey_path = "/path/to/registry-ed25519.pub"
```

`url` and `pubkey_path` are both required for registry entries to appear.
**This build ships no in-binary default public key** — the registry index is
signed by a dedicated key separate from the release-signing key (R-14.75), and
promoting a test fixture to a production trust root would make every real
index look verified without being verified. Until a real key is configured:

- registry entries are never served — the catalog is builtin-only, silently;
- if `url` is set with no `pubkey_path`, a warning is logged and `cascade
  doctor`'s `registry_pubkey` check reports it.

An unreachable, configured registry (a real HTTP/transport failure) is
reported as a command error, not silently swallowed. A configured registry
whose signature does not verify fails closed the same way an absent one does:
builtin catalog only, no error, nothing leaked.

## MCP

`cascade_plugin_search` is the MCP tool name (07-CLI-COMMAND-TREE's
`cascade_<noun>_<verb>` mirror rule), gated by the `plugins.read` capability —
a policy that denies it hides the tool from `tools/list` entirely.
