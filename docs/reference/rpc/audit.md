# Audit RPC methods

Methods that let the CLI append records to the daemon's audit log. Access
classes are listed in the generated RPC method reference. Both
methods refuse unknown fields, are rate limited (a burst fails with
`quota-exhausted`) and answer `invalid-input` for a malformed request.

### `audit.gate_append`

Appends one row describing a local elevation gate event. The request must be
at most 512 bytes and one object with `kind`, `verb`, `params_hash` and, where
the kind allows, `reason` or `name`.

- `kind` is one of `elevation.attempt`, `elevation.grant`, `elevation.deny`
  or `vault.access`, using the audit log's kind names.
- `verb` is a dotted lowercase identifier such as `vault.get`, at most 64
  characters.
- `params_hash` is a 64-character lowercase hex digest.
- An `elevation.deny` row needs a `reason` from the known set of local gate
  reasons or the error kind names; no other kind may carry a reason.
- A `vault.access` row needs a secret `name` and no reason; no other kind may
  carry a name.

Result: `{"seq": n}`, the sequence number of the appended row. A daemon with
no audit log answers `unavailable`.

### `audit.clipboard_effect`

Records the lifecycle of a clipboard write as an audited effect. The request
must be at most 2048 bytes. Params: `op`, plus fields that depend on it.

| `op` | Required | Forbidden |
|---|---|---|
| `begin` | `action` (`clipboard.write` or `clipboard.clear`), `key`, `explain` | `handle`, `detail` |
| `confirm`, `fail` | `handle` | `action`, `key`, `explain` |
| `unknown` | `handle` | `action`, `key`, `explain`, `detail` |
| `refused` | `action` (`clipboard.write.refused`), `explain` | `key`, `handle`, `detail` |

`key` must be `clipboard.write:<id>` or `clipboard.clear:<id>:<id>`, with ULID
ids, and agree with `action`. `explain` is the clipboard explanation: its
`action_class` must match the record, `platform` is a short lowercase word,
and `written_at` and `clear_due_at` are RFC 3339 times that appear together or
not at all. `handle` is the one returned by `begin`: `key`, `intent_seq`
and an optional `intent_id` up to 64 characters. `detail` is a short
single-line string of at most 256 characters.

Result: for `begin`, `{"seq", "handle": {...}}`; for every other `op`,
`{"seq": n}`.
