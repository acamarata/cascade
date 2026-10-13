# Spawn envelope

The driver-facing projection of the [scope envelope](scope-envelope.md). An
external agent driver (claude today; codex, opencode and antigravity follow)
may not start a child until the job's `provider.SpawnEnvelope` passes
`Check`. There is one envelope model: `SpawnEnvelope` is a copy of a verified
`envelope.Envelope`, not a second definition. It lives in `pkg/provider`
because `pkg/envelope` imports `pkg/provider`.

## Projection

`agents.ProjectEnvelope(signed, pinnedKey, now)` in
`internal/providers/agents` is the only production writer. It runs
`envelope.Verify` first (its errors come back unchanged), then copies:

| `SpawnEnvelope` | From the verified envelope |
|---|---|
| `Digest` | `envelope.Digest(env)`: 64 lowercase hex |
| `Principal` | `Principal` |
| `Roots`, `Tools`, `Commands`, `Egress` | the same lists, cloned |
| `Sensitivity` | `Sensitivity` |
| `ExpiresAt` | `ExpiresAt` |

Grants, budget, fence and issuer stay in the signed envelope; a driver does
not need them to refuse an incomplete spawn.

## Check

`Check(worktreeRoot, now)` returns `provider.ErrEnvelopeIncomplete`, wrapped
with the failing field, when:

1. `Digest` is not 64 lowercase hex characters.
2. `Principal` is empty.
3. `Roots` is empty, or a root is not absolute and already clean.
4. `ExpiresAt` is zero or not after `now`.
5. `worktreeRoot` is not absolute and clean, or is neither equal to a root nor
   under one at a path-segment boundary (`/a/bc` is not under `/a/b`).

`Check` is lexical and reads no clock or file system. On Windows an envelope
root is spelled `/C:/dir` (`pkg/envelope` requires a leading slash) and `Check`
maps it to `C:\dir`; a worktree is always the native spelling.

The claude driver runs `Check` with its injected clock after the lease checks
and before the hook-pack copy, the version probe and every child, so a refusal
leaves no child behind. It then resolves symlinks: the leased root must lie at
or under the resolved path of an envelope root, or the spawn is refused with
`ErrEnvelopeIncomplete` (a lease that is a link out of the envelope). A driver
built without a clock can report capabilities but its `Spawn` refuses with
`KindInvalidInput`.

## Model and effort

`AgentJobSpec.Model` and `Effort` are per job. An empty value emits no flag.
A non-empty `Effort` overrides `DriverDefaults.Effort`. Each driver validates
against its own captured closed set: claude accepts `low`, `medium`, `high`,
`xhigh`, `max` for `--effort`, and a plain model name or alias for `--model`
(letters, digits, `. _ : / [ ] -`; never flag-shaped); anything else is
`KindInvalidInput` before the hook-pack copy and every child, including the
version probe.

## NativeEvent

`DriverEvent.Native` and `AgentEvent.Native` carry `NativeEvent{Type, Raw}`:
the harness's own per-event frame, undecoded. It is nil when the driver has no
per-event stream (claude headless json mode). `NormalizeEvent` copies `Raw`.

## Approval requests

`ApprovalRequest` gains `Kind` (`command`, `file_change`, `permissions`) and
`Grant *PermissionGrant{Paths, Hosts}`: what a permissions request would add.

## Binding table

`internal/providers/agents` holds the harness binding (harness id, owning
plugin, driver factory, closed credential table). `Bindings()` is the
read-only listing; a `Resolver` refuses a lane whose plugin is absent or
disabled and never falls back to another harness. Arch gates in
`internal/build` confine driver imports and `Spawn` calls (including through a
generic constraint) to that package, forbid building or writing a
`SpawnEnvelope` (a literal, `new`, a conversion, a field write or an alias)
outside it and the conformance suite, and block constant vendor program names
(any case, with a `.exe`/`.cmd` style suffix) passed to `os/exec` elsewhere (a reflective call or a program name held in a variable
is outside what a type-based gate can see; so are a field write in a package that never names the envelope type, a defined type over it, and a write through a slice handed to a callee, all recorded as follow-ups to harden the gate).
