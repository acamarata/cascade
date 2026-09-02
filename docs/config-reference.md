# config.toml reference: hot-reload rules, baseline divergence, [elevation]

Companion to [`cli-reference/config.md`](cli-reference/config.md) (the
`cascade config` verb reference). This page documents the *behavior* the
write verbs and the hot-reload engine implement
(`internal/runtime/{config_write.go,toml_edit.go,hotreload*.go,baseline*.go}`,
P1-E03-W1-S05-T8), per 08-INIT-CONFIG-SPEC.md §3.

## Hot-reload rules by section

`internal/runtime.HotReloader` re-reads and re-validates `config.toml` on
a `SIGHUP`, a `cascade config reload`, or an fsnotify-detected write
(500ms debounce). Every section falls into one of three classes:

| Section | Reload class | Actually reloads live today? |
|---|---|---|
| `[runtime]` | cold | No live consumer besides the process's own path resolution at startup; a change is diffed and reported via `config.restart.required`, never applied without a restart. |
| `[daemon]` | cold | Same as `[runtime]` — diffed, reported, never live-applied. |
| `[logging]` | hot | **Yes** — the only section with a real runtime consumer (`LogProvider.SetLevel`/`Reconfigure`, `internal/runtime/logger.go`/`rotation.go`). `HotReloader.applyLive` calls both without a restart. |
| `[storage]` | cold | Diffed, reported, never live-applied. |
| `[retrieval]`, `[memory]`, `[conductor]`, `[notify]`, `[nodes]`, `[sync]`, `[telemetry]`, `[hooks]`, `[governor]`, `[registry]`, `[plugins]`, `[plugins.<name>]` | hot (per 08 §3's table) | **Registered, awaiting their owning ticket.** These sections have no typed consumer anywhere in this tree yet (they live only in `Config.Extra`, the generic preserved map). A change to one of them is accepted into the in-memory `*Config` snapshot — so a subsequent `cascade config get`/`list --effective` sees the new value immediately — but nothing today reads `Config.Extra["retrieval"]` (etc.) to actually change behavior. Calling this "hot-reloaded" without qualification would be an Art.1 violation; it is accurate to say the *snapshot* reloads, not that a *behavior* changes, until each section's owning subsystem ticket lands and starts consuming it through `HotReloader.applyLive`. |
| `[policy]`, `[secrets]`, `[sync]` (domain classes), `[nodes]` (`trust_tier`), `[conductor]` (external-routing/spill) | hot, but **tightening-only** | Same "snapshot updates, no live consumer yet" caveat as above — AND every change is additionally run through the loosening gate (next section) before it is even accepted into the snapshot. |
| `[elevation]` | **never hot-reloadable, either direction** | A change to `allow_remote` or `helper_pubkey` — tightening or loosening — is unconditionally rejected via hot-reload. Rotating either requires an out-of-band elevated verb with a valid attestation (not shipped in W1); a config.toml edit alone is never sufficient. |

## Tightening-only enforcement (the loosening gate)

`internal/runtime.CompareSecurity(current, proposed EffectiveConfig)
[]LooseningPath` is the pure classifier behind the gate. It compares six
guarded families — `policy.autonomy_profile`, `secrets.keychain_backend`,
`sync`'s per-domain classes, `nodes.trust_tier`,
`conductor.{external_routing_enabled,spill_enabled}`, and
`elevation.{allow_remote,helper_pubkey}` — and returns every field that is
not clearly no-looser than its current value.

**Rationale for the conservative default:** several of these fields
(`autonomy_profile`, `keychain_backend`, `trust_tier`,
`helper_pubkey`) have no ratified total order anywhere in the P1 planning
corpus as of this ticket — there is no document saying which
`autonomy_profile` string is "tighter" than another. Per §D-27's W1
doctrine ("deny-by-default needs no policy engine"), **any change** to
one of these under-specified fields is treated as a loosening and denied.
Two families *do* have a ratified order and are compared properly:

- `elevation.allow_remote`: `false` is strictly tighter than `true` (08's
  own documented default).
- `sync` domain class: `local-only` < `synced` < `server-primary`
  (00-VISION.md §sync). An unrecognised class name fails closed (treated
  as maximally loose).
- `conductor.external_routing_enabled` / `spill_enabled`: `false` is
  tighter than `true` (enabling either expands what leaves the trust
  boundary).

In W1, **any non-empty `[]LooseningPath` result is an unconditional
deny** — `config.reload.rejected` is emitted, the rejection is persisted
in the audit domain, and the running config is left completely
unchanged. There is no policy-engine escalation path in this tree yet;
the elevation-routed loosening path (an operator explicitly authorizing a
looser config) is out of scope for this ticket and tracked as
allowed-fail against I/S-18.T5 (§D-27).

## Cold-key change: `config.restart.required`

After `config validate` passes, `HotReloader` diffs `[runtime]`,
`[daemon]`, and `[storage]` specifically (the cold-key detection the
ticket contract names) between the running config and the proposed one.
If any key in those three sections changed, the changed keys are named in
a `config.restart.required` event, and the *cold* sections are frozen at
their old values in the swapped-in snapshot — only the hot parts of the
same edit are applied. A user who changes both a hot and a cold key in
one `config.toml` edit gets the hot part applied immediately and a clear
signal that a restart is needed for the rest; nothing is silently
dropped and nothing partially-applies without saying so.

## Boot-time baseline divergence

Once at daemon boot, after config load, `internal/runtime.BaselineChecker`
computes a canonical SHA-256 over the same six guarded families
(`ComputeSectionsHash`) and compares it against the last-persisted
baseline record in the audit domain (`pkg/provider.Store`, namespace
`"audit"`, key `config_baseline`):

- **No baseline record found** (first boot, or the record was lost) →
  fail closed: `MostRestrictiveDefaults()` (all guarded fields at their
  safest value — `elevation.allow_remote=false`, empty sync-class map,
  etc.) is used for the six guarded families regardless of what's on
  disk, a `config.policy.divergent` event fires with
  `{reason: "baseline_missing"}`, and a `doctor_error` record is
  persisted (`cascade doctor` surfaces it).
- **Hash mismatch, on-disk is looser** → same fail-closed treatment,
  `{reason: "baseline_divergent", expected_hash, actual_hash}`.
- **Hash mismatch, on-disk is tighter** → proceeds normally with the
  on-disk config, and the baseline record is rewritten to the new,
  tighter hash.
- **Unrecognised `schema_version`** (`v > 1`) on the persisted record is
  tolerated — a warning is surfaced, not an error — so a future signed
  v2 record (D/S-07.T6) is forward-compatible with this reader.

**Recovery from a divergent-boot state:** re-authorize the tighter
baseline via an elevated verb (not shipped in this ticket — D/S-07.T6's
territory) that re-signs and re-persists the baseline record at the
current on-disk hash. Until that lands, the only way out of a
divergent-boot state in this tree is to restore `config.toml` to a
configuration that is no looser than the last-recorded baseline.

## `[elevation]` — why it is never hot-reloadable

```toml
[elevation]
allow_remote = false        # break-glass for server profile
helper_pubkey = "…"         # enrolled at init (TOFU + printed fingerprint)
```

Both fields require an existing valid attestation to change — a plain
`config.toml` edit is never sufficient, in either direction. This is
stricter than the tightening-only rule every other guarded family gets:
even a strictly-tightening `allow_remote: true → false` change is
rejected via hot-reload, because the *mechanism* of change (an
unattested file edit) is what's disallowed, not just the direction. The
CLI write verbs (`set`/`unset`/`edit`) can still *write* `[elevation]`
keys to `config.toml` on disk (subject to the same TOML-literal parsing
and shape validation as any other key) — what's refused is applying that
change live without a restart-and-reattestation cycle.

## Round-trip fidelity: comments and key order survive `config set`

`cascade config set`/`unset` never round-trip through
`toml.Marshal`/`Unmarshal` to produce their output —
`github.com/pelletier/go-toml/v2` does not preserve comments or key order
across a decode/encode cycle (verified against its docs and behavior).
Instead, `internal/runtime/toml_edit.go`'s line-level editor locates the
target key's exact line (or its enclosing table's line range) and
rewrites only that line's value side, leaving every comment, blank line,
and surrounding key untouched byte-for-byte. The one case that adds new
content rather than rewriting existing content is a brand-new
`[table]` section for a key whose table does not exist yet.

**Known limitation:** the line-level editor only recognises single-line
values. A value that legitimately spans multiple lines in the source file
(a multi-line basic/literal string, or an array broken across lines) is
not matched for in-place rewrite; `set` on such a key falls back to
inserting a new line rather than editing the existing multi-line one,
which is additive (never silently drops or corrupts the old value) but
does leave the old multi-line value in the file alongside the new
single-line one until a human cleans it up. This is documented here
deliberately, per this ticket's contract, rather than silently shipped:
choosing structure-preservation over canonical-rewrite is a real design
trade — the alternative (always canonically re-serializing) would
silently destroy every user's hand-written comments on the very first
`config set`, which is worse.

## Atomicity: a crashed write never corrupts `config.toml`

Every write this ticket performs — `set`, `unset`, `edit`'s apply step —
goes through the same temp-file-in-the-same-directory-then-rename
pattern `internal/runtime/config_load.go`'s pre-existing
`writeConfigAtomic` established (R-14.106 precedent): the new content is
written to a `.config-*.toml.tmp` file in the same directory first, then
atomically renamed over the real path. A process crash or power loss
between those two steps leaves either the untouched original file or an
orphaned temp file — never a truncated or half-written `config.toml`.
Proven in `internal/runtime/toml_edit_test.go`'s
`TestConfigWriter_CrashSimulation_TempFileNeverReplacesOnFailure` (a
failed `Validate` step never even reaches the write call, so the original
file's bytes are provably byte-for-byte unchanged) and by construction
for the rename step itself (POSIX/Windows rename is atomic within one
filesystem).
