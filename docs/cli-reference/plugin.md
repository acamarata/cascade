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

# `cascade plugin update`

```
cascade plugin update <name> [--yes] [--accept-grant=<grant> ...]
cascade plugin update <name> --from <path> [--checksum <hash>]
```

Two candidate sources, selected by whether `--from` is given — **both commit
through the exact same elevation-gated path** (`internal/plugins.UpdatePlugin`);
the registry path is NOT exempt from the daemon-mediated elevation flow the
`--from` path uses:

- **No `--from`** (the default): the registry-driven path. Checks the signed
  `[registry]` index for a newer published version of `<name>` than what is
  installed, downloads and fully verifies the candidate artifact — checksum
  **and** Ed25519 signature, via the registry client's own verifier, before
  anything else touches it (an artifact whose entry carries an empty
  signature is refused, not treated as unsigned-but-fine) — computes the
  capability-grant diff against the installed manifest, and only then calls
  `UpdatePlugin`.
- **`--from <path>`**: the original local-manifest path — reads a candidate
  manifest from disk and checksum-pins it with `--checksum`, then calls the
  identical `UpdatePlugin`.

`UpdatePlugin` refuses to commit an update that would be **elevated** —
either a capability-grant expansion, or ANY change to the plugin's runtime
tier (builtin/process/wasm/remote, not only a move to process) relative to
what is currently installed — unless a daemon is available; even with a
daemon, this build has no attestation flow yet, so an elevated update is
reported ("... requests new capabilities: ... — elevation required") rather
than committed. `--accept-grant` (below) is required before the registry
path will even ATTEMPT an expanding update, but it is never itself the
authority that lets one commit.

Like `plugin search`, the registry-driven path refuses rather than silently
bypassing a **configured** daemon (no `plugin.update` RPC method exists yet
to route the registry check through it): update via `--from`, or run without
a configured daemon, in that case.

## Already at the latest version

```
$ cascade plugin update grant-diff-demo
no update available
```

Exit 0 — a no-op, not an error (idempotent: running it again reports the
same thing).

## Grant-diff re-confirmation

When the candidate manifest requests a capability the installed one does
not have, the diff is printed before anything is written:

```
$ cascade plugin update grant-diff-demo
grant-diff-demo requests new capabilities: network.egress
Error: registry: update requests new capability grants that were not
explicitly accepted: missing --accept-grant for: network.egress ...
```

`--yes` accepts every other non-interactive default, but **never** a grant
expansion — each new capability needs its own explicit flag. On a host with
no daemon configured (the common case), an expanding update still refuses
even WITH `--accept-grant` — the flag is the required acknowledgement, not
the elevation authority:

```
$ cascade plugin update grant-diff-demo --yes --accept-grant=network.egress
grant-diff-demo requests new capabilities: network.egress
Error: unavailable: cascade plugin update needs a running daemon: daemon
required for elevated plugin operations. Start it with `cascade daemon start`.
```

A removed capability is shown too (`... no longer requires: <grant>`) and
never itself needs `--accept-grant` or `--yes` — only an ADDED capability is
a would-prompt moment.

No diff at all (the candidate requests nothing new and drops nothing) needs
no `--accept-grant` and no `--yes`; it proceeds directly.

`CASCADE_NO_INPUT=1` without `--yes`, on an update that has an ADDED
capability to confirm, hard-errors at that point rather than silently
accepting or silently refusing — the same §5.8 automation-parity contract
every other elevated plugin verb follows.

## Checksum and signature mismatch

A candidate artifact whose bytes do not match the registry's published
checksum for that version, or whose Ed25519 signature does not verify (or
is missing), is never installed: the download is discarded, its staging
file removed, and the command exits non-zero. The previously installed
version is untouched.

## Pre-release versions

This build compares only `major.minor.patch`; it has no dependency capable
of ordering pre-release identifiers (`-rc1`, `-beta1`, ...) correctly. A
comparison where EITHER the installed version or the registry's published
version carries a pre-release suffix is refused outright, naming both
versions, rather than silently guessing an order that might be wrong in
either direction.

## Runtime-tier changes

A candidate manifest that changes the plugin's runtime tier relative to
what is installed (for example `builtin` to `process`) is elevated exactly
like a grant expansion, even when the capability set is otherwise
unchanged — the installed tier is itself a security boundary the operator
chose.
