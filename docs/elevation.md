# Elevation

Elevated verbs require custody that proves local presence. The selector ranks sources by their registered custody tier, independently of the keystore's storage label or registration order.

| Custody | Elevation |
| --- | --- |
| platform | Allowed through the protected platform source |
| presence | Allowed through a registered presence source |
| file | Refused in release builds; signing exists only with the `devkeys` build tag |
| none | Refused |

A file key that reports an OS keychain storage label remains file custody. An existing file key never outranks an available platform or presence source. Enrollment never falls back to file storage after a protected source fails.

## Release availability

The cgo-free macOS and Linux release builds refuse elevated operations until a protected helper or presence source is available. Linux PAM and keyring storage do not qualify as platform custody: the stored key can be read without authentication. Windows remains tier 2 and refuses elevation.

Custody refusals use `KindUnsupported`, JSON-RPC code `-32012`, and data containing `reason: ELEVATION_CUSTODY_TIER` and the selected tier. They do not issue a challenge or run an elevated handler. The read-only custody report exposes the selected tier, source, enrollment and binding status; doctor integration is separate.

## Enrollment and stale file keys

The enrolled public key must equal the selected custody key. A swapped trust record refuses. On POSIX, trust records must have mode `0600` and belong to the effective user.

When a protected source becomes available, an old enrollment bound to `elevation.key` requires explicit replacement:

```text
cascade elevate-helper --enroll --replace-file-tier
```

Replacement is allowed only when the old trust record matches the file key. It stores the new trust record before removing the stale key. A record for an unrelated key remains untouched.

## Signing

Authentication and signing share one operation. Attestations bind the request, the RPC method, the action and the nonce, and expire after five minutes. Nonces are single use. The `devkeys` tag permits file signing for development; it does not prove local presence and is unsuitable for release artifacts.

The signed bytes are the compact JSON object `{action_hash, exp_unix, issued_unix, method, nonce, pubkey_fingerprint, request_id}`, keys in that order. `action_hash` is the SHA-256 of the raw params. `method` names the verb the attestation authorizes. The verifier compares the signed method with the method being dispatched, and the ledger-free check provided for a persisted attestation record (no expiry, no nonce) compares it with the expected method, so an attestation for one verb never verifies for another verb with byte-identical params. An attestation without a method, including one signed in the earlier six-field format, is refused. `cascade elevate-helper --sign` takes `<request-id> <method> <action-hash> <nonce>`. The format has not been released, so nothing is migrated: enrolled keys and trust records stay valid, the `cascade-elevate-helper` binary signs whatever bytes it is given and needs no change, and an attestation made by an older `cascade` binary is refused until that binary is rebuilt.

## Daemon enforcement

The daemon registry runs one elevation middleware, mounted first in the chain (`cmd/cascade/wire_elevation.go`). It is built once at start from the selected custody, the trust record bound to that custody key, one nonce ledger and the process's single redacting audit log. Enrolling a key needs a daemon restart.

For an elevated verb the middleware works in this order: custody gate, platform check, trust binding, audit log present; then either a challenge (a fresh nonce and an `attempt` record) or a verification (an `attempt` record, the attestation check, then a `grant` or `deny` record). A record that cannot be written refuses the call, and the handler never runs after a failed `grant` append. Records carry the method and a hash of the params, never the params. A custody tier that cannot elevate, or a trust record that is missing or names another key, refuses every elevated verb before a nonce is issued.

A verified call reaches its handler with a `VerifiedElevation` in its context: the verified method, the hash of the exact params, and the time. Nothing outside `internal/rpc` can build one, and a witness is only minted by a middleware whose deps carry a `WitnessIssuer`; the witness remembers that issuer. The daemon creates one issuer beside its bound custody and exposes only a verify-only `WitnessVerifier` as `ElevationStatus.Verifier`; the minting issuer never leaves the daemon package. `policy.WitnessAttestor(verifier)` accepts only witnesses minted under that issuer and only for the verb it is asked about, so a ceremony gate with its own trust store (backup, plugin install) can never vouch for a policy verb. A gate built without an issuer still verifies and audits, but its handler runs with no witness.

An attested call claims its nonce before any audit record is written: a nonce the ledger does not hold (never issued, replayed, reaped, burnt) or that a concurrent call is already verifying is refused with no record. A pending nonce is burnt after one failed verification: it stays in the ledger as a tombstone and keeps its slot until it expires, so a challenge buys at most one verification record per slot and the ledger bound (1024 entries per five minutes) is also the bound on anonymous audit appends. The elevation table seals on its first read or on the first `Registry.Dispatch` of any registry, whichever comes first, including a dispatch of an unknown method or on a registry without the middleware.

Envelope rule: for a verb that is in the table at all, params with an `_attestation` or `_args` key are always verified, and the handler receives `_args` only. A conditional rule is read for envelope-free params, so a loosening request cannot hide behind an envelope. Key matching ignores case, as the handlers' own decoding does, and keys that fold together make the params unreadable, which elevates.

The nonce ledger drops expired nonces on each issue and refuses with `KindUnavailable` beyond 1024 pending. An `attempt` record is written only when a nonce is issued or an attestation is about to be verified, so anonymous callers cannot grow the audit log faster than the ledger allows.

### Verbs

Always elevated: `vault.get`, `vault.rotate`, `approval.grant`, `standing_grant.create`, `standing_grant.change`, `backup.create`, `backup.export`, `backup.import`, `backup.restore`, `backup.key_export`, `backup.key_import`, `plugin.add`, `plugin.update`, `perms.grant`, `perms.revoke`, `node.enroll`, `node.remove`, `node.upgrade`, `node.rotate_key`, `node.revoke`, `uninstall.purge_data`, `config.accept_loosening`, `policy.authorize`.

Elevated when the request loosens or discards: `sync.conflicts_resolve` (anything but keeping the server side), `policy.set` and `sensitivity.set` (`direction` is `loosen` or unreadable), `elevation.set_allow_remote` and `plugins.set_enable_remote_runtime` (a flag that turns on, or cannot be read).

`plugin.add` used to be elevated only when the request named `process_tier` or `grant_expand`. The CLI sends neither, so installs ran unattested. It is now always elevated.

A later ticket adds a verb with `rpc.RegisterElevatedVerb(method, rpc.ElevationRule{Always: true})` from the file that registers its handler. The call panics on an empty method, a duplicate, a rule that elevates nothing, a rule with both forms, and any call after the table is sealed. The table seals on its first read or first dispatch. Policy derives its local-only verb set from the table at call time, so a registered verb is never bridge-approvable or standing. New rows stay `Always` unless the handler computes loosening on the server from stored state; a client-declared flag never narrows a rule.

### Other registries

| Registry | Elevation |
| --- | --- |
| Daemon | The middleware above, over the bound custody |
| MCP tool registry and MCP socket | Refuse-all: these processes hold no custody session |
| `cascade node serve` | Refuse-all by default (`ServeDeps.Elevation` nil): the serve process is separate from the daemon and shares none of its ledger, trust or audit log, so `node.enroll` over it always refuses. Local `cascade node enroll` is unchanged |

Refuse-all returns the custody refusal at tier `none` (`-32012`, no nonce).

### CLI signing

Every elevated CLI verb dials the daemon through `callElevated`: the first call goes out unsigned, a challenge carries the nonce, and one signed retry follows. A second challenge is `ErrElevationNotSatisfied`.

## Local elevated verbs

Some elevated verbs run in the CLI process without the daemon. Each one passes the local gate before it reads or writes anything. The gate returns success only after it has verified a signature over a nonce it issued during the same call.

| Command | Verb |
| --- | --- |
| `vault get`, `vault grant` | `vault.get` |
| `vault rotate` | `vault.rotate` |
| `node enroll`, `node remove`, `node upgrade` | `node.enroll`, `node.remove`, `node.upgrade` |
| `node rotate-key`, `node revoke` | `node.rotate_key`, `node.revoke` |
| `plugin add`, no daemon running | `plugin.add` |
| `plugin update`, no daemon running | `plugin.update` |

The plugin commands ask for the proof only when they are about to write the plugin store. An add that is already installed writes nothing and asks for nothing. While a daemon runs it holds the plugin store, so `plugin add`, `plugin update` and `plugin perms grant|revoke` never open it: they are the daemon's attested `plugin.add`, `plugin.update`, `perms.grant` and `perms.revoke` calls. `plugin perms` refuses without a daemon. A plugin command's own refusals (no daemon where one is needed, `CASCADE_NO_INPUT=1`) are recorded like the gate's, with reasons `daemon_required` and `no_input`. On Linux the local signer can be the passphrase presence key: see [the presence tier](elevation-presence.md).

The backup commands run their own in-process ceremony, which uses the same challenge and signing code. `CASCADE_NO_INPUT=1` refuses them before any custody is selected.

For one call the gate:

1. Refuses when `CASCADE_NO_INPUT=1`, before any custody is opened.
2. Selects custody once. A tier that cannot elevate gets the typed custody refusal, and the gate never asks it to sign.
3. Binds the trust record to that custody key through `elevation.BoundTrust`. A missing, swapped or stale record refuses.
4. Refuses a verb that the elevation table does not list, with `ELEVATION_REQUIRED`. Prefixed names such as `local.vault.get` are not table verbs.
5. Builds an in-process elevation middleware over a fresh nonce ledger, the bound trust and the gate's audit sink. It issues a challenge with method set to the verb and params exactly `{"verb":<verb>,"local":true}`.
6. Signs the challenge through `signAttestation`, then verifies the result through the same middleware. The handler grants only when it holds that middleware's witness for the verb and params hash.

A granted call writes three rows: `elevation.attempt` when the challenge is issued, `elevation.attempt` when the signed answer is verified, then `elevation.grant`. A refused call writes `elevation.attempt` (one, or two when the refusal comes at verification) and then `elevation.deny`, including calls refused before any challenge. The deny carries a constant reason:

| Reason | Refusal |
| --- | --- |
| `no_input` | `CASCADE_NO_INPUT=1` |
| `custody_tier` | the selected custody cannot elevate |
| `unbound` | no trust record is bound to the custody key |
| `not_elevated` | the verb is not in the elevation table |
| `sign_failed`, `no_signature` | the signer returned an error, or no signature |
| `invalid-input`, `not-found`, other kinds | the middleware refused the attestation. A replayed or foreign nonce is `not-found` |
| `daemon_required` | a plugin command that needs the daemon ran without one |

### Where the rows go

Gate rows have a closed vocabulary: the row kind, the verb, a reason code, a params hash and, for `vault.access` only, the secret's name, which must match the secret-name grammar. A row carries no free text. Rows are appended through a log with no redactor, so writing one never reads the vault or any custody.

While a daemon runs, it holds the runtime store, so the CLI sends each row to the daemon's `audit.gate_append` verb. That verb is owner only: a caller with a principal, or no caller at all, is refused. It is not elevated, because it grants nothing. It accepts exactly one row of at most 512 bytes, with no unknown fields, and validates every field. It admits 10 rows a second with a burst of 20, and appends them into the same audit chain as the daemon's own records. The daemon acknowledges each row with its sequence number, and a row without an acknowledgement refuses the call. With no daemon listening on the socket, the CLI appends to the store itself, opening and closing it for each row. A daemon that answered once and then fails refuses the call; the CLI never switches to the store mid-call.

If an audit write fails, the call is refused, and the error reports the failed write with the code `audit_unavailable`.

### Threat model for gate rows

Every gate row carries `source: cli-gate` in its explanation. Its actor is `elevation.local`, or `cli` for `vault.access`. A gate row is written by a CLI process, and the daemon does not verify it. The daemon's own middleware rows use actor `elevation` and carry no `source`, and `audit.gate_append` refuses any row that names an actor or a source. A gate row therefore can never pose as a daemon row.

Under the ADRs, the same user ID is not a trust boundary. Any process running as the owner can reach the daemon socket, so it can append forged `cli-gate` rows, just as it could write the store directly while the daemon is down. `audit.gate_append` is a token bucket: 10 rows a second with a burst of 20. A flood is spread out instead of landing at once. A same-user process can still starve a CLI call by flooding steadily. That process could equally kill the daemon, so this is not a new boundary.

Audit-log retention is not covered: gate rows, like every audit row, are kept without limit. No ticket covers audit-log retention yet; it is a follow-up for planning. Because the ledger lives for one call only, an attestation from an earlier call or over another nonce never verifies. `vault get` writes `vault.access` (the secret's name and the verb, outcome `requested`) before it opens or reads the vault. If that record cannot be stored, nothing is read, and `vault get: refused (audit_unavailable)` is printed on standard error.

`node.rotate_key` and `node.revoke` have no RPC handler. They are rows of the elevation table only so the local gate can challenge them like every other elevated verb.

Production code builds every trust backend as an argument of `BoundTrust` or of an enrolment writer (`EnrollPresence`, `ReplaceFileTierEnrollment`, `NewElevationTrustStore(...).Enroll`). `TestProductionTrustStoresAreBound` enforces this.
