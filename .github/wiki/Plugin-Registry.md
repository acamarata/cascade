# Plugin Registry

Cascade ships a fixed set of builtin plugins (compiled in, always active) and
can optionally discover more from a signed registry index.

## Searching the catalog

```
cascade plugin search [<query>]
cascade plugin search --json
```

An empty query browses the whole catalog: every builtin plugin, plus every
registry entry when a verified registry is configured. A query filters by
substring match against name, description, and (for registry entries) tags.

Full command reference: [`docs/cli-reference/plugin.md`](../../docs/cli-reference/plugin.md).

Programmatically, the same catalog is reachable as the `cascade_plugin_search`
MCP tool.

## Configuring a registry

```toml
[registry]
url = "https://your-registry.example/index"
pubkey_path = "/path/to/registry-signing-key.pub"
```

The index is a static, signed JSON document — `pkg/plugin.RegistryIndex` —
verified with Ed25519 before any entry in it is trusted or cached. Signing key
management (generating an index, publishing it, rotating the key) is
publisher-side tooling, not a `cascade` subcommand; see
`internal/tools/registry-gen` for the reference generator.

**No production registry is configured by default.** Shipping one requires a
real, dedicated Ed25519 signing keypair — separate from the release-signing
key — which this project has not yet generated. Until it has, `cascade plugin
search` and the `cascade init` catalog page both answer from the builtin set
only, and `cascade doctor` names the gap (`registry_pubkey` check) whenever
`[registry].url` is set without a `[registry].pubkey_path`.

## `cascade init`'s plugin page

Step 4 of the setup wizard renders the same catalog `plugin search` does — a
verified registry entry appears there too, unchecked by default (a discovered,
opt-in offering), next to the always-present, always-checked builtins.

## Updating a plugin

```
cascade plugin update <name> [--yes] [--accept-grant=<grant> ...]
```

`cascade plugin update` checks the registry for a newer published version of
an installed plugin, downloads and fully verifies the candidate artifact —
checksum **and** Ed25519 signature — **before** anything else looks at it,
and — only if that succeeds — compares the candidate manifest's requested
capabilities against what is currently granted. Both this path and the
local-manifest `--from <path>` form below commit through the SAME
elevation-gated function (`internal/plugins.UpdatePlugin`); the registry
path is not a separate, lighter-weight commit path.

- **Already at the latest version**: `no update available`, exit 0 — a
  no-op, safe to run repeatedly (idempotent).
- **Pre-release versions**: a comparison where either the installed or the
  registry's published version carries a pre-release suffix (`-rc1`, ...)
  is refused, naming both versions — this build has no dependency capable
  of ordering pre-release identifiers correctly, so it refuses rather than
  guesses.
- **No new capabilities requested, and the runtime tier is unchanged**: the
  update proceeds without asking.
- **New capabilities requested, OR the runtime tier changes** (e.g.
  `builtin` to `process`, even with the same capability set): elevation is
  required. A grant diff is printed before anything is written; `--yes`
  accepts ordinary non-interactive defaults but never a grant expansion by
  itself — each new capability needs its own `--accept-grant=<grant>`, and
  even with it, an expanding or tier-changing update still refuses on a
  host with no daemon configured (the acknowledgement is required, but it
  is never the elevation authority). Omitting `--accept-grant` refuses with
  a non-zero exit naming exactly what is missing. `CASCADE_NO_INPUT=1`
  without `--yes` hard-errors at this point rather than silently choosing
  either way. A removed capability is shown too but never itself needs
  `--accept-grant`.
- **Checksum or signature mismatch**: the candidate is discarded, its
  staging file removed, and the previously installed version is left
  exactly as it was — nothing partial is ever left behind or installed.

A local-manifest candidate (bypassing the registry) is still available via
`--from <path>`; see [`docs/cli-reference/plugin.md`](../../docs/cli-reference/plugin.md)
for both forms in full.

## Conversational install

cascade-pa declares the `cascade.install_flow` intent, which walks an
unresolved intent (e.g. "install the thing that does X") through four
ordered phases:

1. **Propose** — the intent resolver picks the one matching plugin (an
   already-installed candidate, or a registry entry) and echoes a proposal
   — name, version, declared permissions, runtime — as a real conversation
   turn, before anything is installed.
2. **Confirm** — the proposal is queued as an L2 (workspace-mutation) ask
   through the same approval queue the CLI/RPC approval surface drives.
   Approving or declining is a human decision made through that surface,
   not a free-text chat reply.
3. **Install** — on approval, the SAME deterministic install path `cascade
   plugin add` uses: checksum- and signature-verified artifact fetch,
   permission-diff confirmation, and (for a process-tier or
   grant-expanding candidate) the local elevation flow. The chat
   confirmation from step 2 never substitutes for elevation — a candidate
   that needs it is refused until a genuine nonce → local-auth → signed
   attestation round trip succeeds.
4. **Resume** — once the plugin is installed (or was already installed and
   enabled), the original intent resumes.

**`CASCADE_NO_INPUT=1`** skips the Confirm step entirely and auto-declines
with a clear error — it never auto-accepts (matching every other
non-interactive `cascade` command's contract).

**Idempotency**: re-triggering the same intent against an already-installed,
already-enabled plugin skips the install step and resumes immediately —
a safe no-op, not a second install attempt.

Declining, an install failure, or an unapproved elevation all stop before
the intent resumes — a failed or declined install never leaves the
original agent action waiting on a plugin that was not actually installed.

## Acceptance: "link my GitHub" end to end

`plugins/cascade-pa/install/acceptance_x_test.go` proves the conversational
install flow above against real, provenance-stamped fixtures rather than
mocked collaborators: a real Ed25519-signed registry index naming
cascade-github (`testdata/acceptance/`, see that directory's `README.md` for
signing provenance), a real intent resolver, a real elevation broker
(genuine Ed25519 key generation, real nonce/attestation round trip), and
the real plugin-add lifecycle functions — the same ones `cascade plugin
add` calls.

- **Happy path** (`TestAcceptance_X_LinkGitHub`): "link my GitHub" resolves
  to cascade-github, a proposal is echoed before anything installs, the
  operator confirms, and — because cascade-github is a process-tier plugin
  — elevation is required. One subtest proves the install is refused when
  no elevation broker is configured at all; the other drives a genuine
  broker to a verified approval and shows the elevation witness reaches
  the retried install call only after that approval, never before.
- **Known limit, proven rather than hidden**: no plugin in this tree can
  actually finish a process-tier elevated install yet — the host has no
  mechanism to mark any manifest's trust tier above "untrusted", so the
  real install lifecycle refuses at that gate every time, for every
  process-tier plugin, not just cascade-github. The acceptance test
  asserts this real, current refusal (and that zero installed-metadata
  record is left behind) rather than faking a successful "tools are live"
  outcome. Separately, the MCP tool surface only ever sources tools from
  the compile-time builtin registry — an installed process-tier plugin's
  declared tools have no path into it yet, mounted or not. Both are
  tracked as open host-mount gaps, not defects in this flow.
- **Already installed** (`TestAcceptance_X_AlreadyInstalled`): a candidate
  already on record as installed skips elevation entirely — the add
  lifecycle's own idempotency check short-circuits before the elevation
  decision is ever evaluated — and resumes immediately, proving the §5.9
  idempotency contract on a real, passing run.
- **Error paths**: a registry fetch failure, a checksum-tampered artifact,
  and an explicit decline each produce zero install attempts and no
  resume, exercising the real registry client's sentinel errors and the
  real artifact verifier.
