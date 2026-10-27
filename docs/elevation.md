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

Always elevated: `vault.get`, `vault.rotate`, `approval.grant`, `standing_grant.create`, `standing_grant.change`, `backup.create`, `backup.export`, `backup.import`, `backup.restore`, `backup.key_export`, `backup.key_import`, `plugin.add`, `plugin.update`, `perms.grant`, `perms.revoke`, `node.enroll`, `node.remove`, `node.upgrade`, `uninstall.purge_data`, `config.accept_loosening`, `policy.authorize`.

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
