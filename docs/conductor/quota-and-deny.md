# Quota and the owner deny list

The daemon builds one conductor quota policy and one owner deny store, both
over the HotReloader's current (effective) configuration. Nothing reads
`config.toml` directly for either. Code: `cmd/cascade/conductor_deny_store.go`,
`internal/runtime/conductor_deny.go`, `internal/conductor/quota*.go`.

## `[conductor].deny_task_classes`

A string array of task class names, default `[]`. A request whose task class
is listed is refused with `KindPolicyDenied` before any provider call, on
every door verb (`denied by [conductor].deny_task_classes`).

- Read on every call from the live config, so an accepted reload applies to
  the next request.
- A value that is not an array of non-empty strings, or names a task class
  the conductor does not know, refuses start. If such a value reaches the
  live config, every class is refused (`deny configuration unavailable:
  <cause>`) and `status.get` shows `conductor.policy` as failed. A read or
  parse error never allows.
- A wiring with no composed daemon behind it has no live configuration and
  refuses every class.

## `[conductor.quota]`

| Key | Type | Default | Meaning |
|---|---|---|---|
| `spill_order` | array of lane names | `[]` | Preference order the router tries lanes in. |
| `ceiling.<lane>` | non-negative integer | none (no cap) | At most this many admissions of `<lane>` per rolling `CeilingWindow`. |
| `personal_tracking` | bool | `false` | Must stay `false`: personal-lane usage is never tracked. |

`CeilingWindow` is fixed at one hour (`internal/conductor/quota_ceiling.go`).
The window is rolling: an admission older than one hour no longer counts. A
lane with no `ceiling` entry has no cap. When every candidate lane is at its
ceiling, the call fails with `KindQuotaExhausted` and the message names the
lanes at their ceiling.

The quota is parsed once at start (a value that does not parse refuses start)
and re-applied on every accepted reload through `QuotaPolicy.Apply`, which
keeps the admission counters. A reloaded value that does not parse keeps the
previous quota and logs the key `conductor.quota`.

## Loosening

Both keys belong to the guarded `[conductor]` family (see
[config-reference.md](../config-reference.md#guarded-families-keys-that-need-attestation-to-loosen)).
Removing a `deny_task_classes` entry, and removing or raising a lane
`ceiling`, are loosening changes: a hot reload refuses them and a restart
runs the tighter value until `config.accept_loosening` admits the file.
Adding a deny entry or adding or lowering a ceiling tightens and applies at
once. `spill_order` is exempt: it only orders lanes the router already
admits, and every ceiling, deny and sensitivity rule still applies to each
lane it names.
