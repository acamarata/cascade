# Approval records

An approval is spendable only as a record the daemon signed itself, and only
once.

## The key

- Ed25519, stored in the vault under `cascade_approval_key`.
- Generated on the daemon's first start when custody reports the entry
  absent. An existing entry is read, never replaced. The absence check and
  the write hold one cross-process lock scoped to the custody the key lives
  in (the data directory for the file vault; a per-user lock keyed by
  backend and service for an OS keystore), so two processes cannot both
  create it. The daemon re-reads the key after writing it and refuses if
  custody holds a different one.
- Every file-vault write (set, delete, imports, provisioning) holds one
  cross-process lock around its whole read-modify-write, so no writer can
  drop an entry another just stored. The vault's own key file is created
  under the same lock and only if absent, so two processes opening a fresh
  vault cannot leave it encrypted under a key nobody holds.
- A stored value that is not a well-formed Ed25519 private key is an
  integrity failure. The daemon generates nothing, writes nothing, and keeps
  approvals closed. A custody read that fails for another reason (a locked
  keychain, say) is reported as it is, and nothing is generated.
- `cascade_approval_key` and `cascade_installation_key` are reserved names.
  `vault set` (every collision mode), `vault rotate`, `vault delete`,
  `vault import`, the v1 migration import and a backup import refuse them
  with a permission error before writing anything. `vault get` and a read
  under a `vault grant` refuse them too.
- The reserved keys are not egress material: the egress vault does not list
  them, and the shared non-elevated read path (rehydration, egress, doctor
  config checks) refuses them. Within cascade only the daemon's key reader
  and provisioning read them. Anyone with direct access to the custody
  backend itself (the OS keychain, or the vault file and its key) can still
  read them; that is the custody boundary, not a cascade verb.
- A backup export leaves the reserved keys out, and a backup import naming
  one is refused before its first write.
- The doctor checks for the key with a read-only custody selection that
  writes no probe item and no vault file.

## Minting

Each queued approval is signed when it is admitted. The request id and
nonce the queue tracks are the ones inside the signed record. The signed
bytes are held in daemon memory with the entry. They are never serialized
into list or show output, bridge payloads, events or the journal.

A daemon whose key failed mints nothing: every ask verdict that would have
queued an approval is downgraded to a deny.

## Granting and redemption

Approving and redeeming are separate steps, done by different parties:

1. An owner (the component that needs the action, such as the plugin
   install gate or the completion gate) queues the request and keeps its
   nonce.
2. A human approves it with `cascade approval grant <id>` (no `--token`).
   This records the decision through `Decide`, bound to the summary and rung
   the entry itself carries, and redeems nothing. It answers
   `{"request_id": ..., "state": "approved"}`.
3. The owner redeems the approved entry with its own nonce, exactly once.

The tokenless grant is an elevated verb. On top of that:

1. The call must carry the elevation middleware's witness for
   `approval.grant`, minted by the daemon's own issuer. A witness for another
   verb, or none, is refused and the entry stays pending.
2. The daemon-held record must verify under the daemon's key, be unexpired
   and name the request being granted. A consumed request has no held
   record and is refused here.
3. The sealed audit history, effect index included, must verify.
4. The decision is claimed as the audit effect `approval.grant:<id>`. The
   claim is create-only, so of any number of concurrent grants exactly one
   proceeds, and a second grant of the same request is refused.
5. The entry must be pending. A denied, canceled or expired entry is refused
   with its own state's error and the effect ends failed.
6. The approval is recorded, then the effect is confirmed. Nothing on this
   path consumes the entry.

With `--token`, the caller holds a signed record and runs the action
itself: the record is verified first, the entry must already be approved,
live and bound to the action being run (checked without consuming, so a
mistyped `--action` spends nothing), and only then is it consumed once.

On restart, an `approval.grant` intent with no terminal record is marked
`unknown-outcome`. Its key stays claimed, so that request is never granted
again. Every refusal is a permission error and writes an `approval.deny`
audit row without any token bytes.

`approval.show` reports any retained entry's state: `pending`, `approved`,
`consuming`, `consumed`, `denied`, `expired` or `canceled`. It never shows a
nonce, token, signed record or digest.

## Known limits

- On macOS the keychain write replaces an existing item. The per-user lock
  (a 0700 directory derived from the uid, not from HOME) covers every
  cascade process of that user; a different tool writing the same keychain
  item is outside it.
- Another local user can pre-create /tmp/cascade-<uid>, and a private /tmp
  (PrivateTmp, sandbox) gives a separate lock. Both fail closed: provisioning
  refuses, or mints refuse. The shipped systemd unit has no PrivateTmp and
  only the daemon provisions.
- An older cascade binary (no locks, no reserved names) or any non-cascade
  writer of the vault or keystore can still overwrite or erase the key.
  That failure is closed, not open: the daemon verifies every record it
  mints against its key, so a replaced or missing key stops approvals
  (status shows `approval-keys` failed or every mint refuses) until the
  daemon restarts and provisions again; it never makes a forged approval
  verify.

- A backup does not carry the approval key. A restored installation
  provisions its own key on first start; approvals signed by the old key do
  not carry over, which is intended (they expire within five minutes anyway).
- A grant that fails after its effect was claimed (the entry not pending,
  the queue changing between the read and the decision, or a storage
  failure) leaves that request ungrantable; queue the action again. A claim
  is never given back.
- An approved entry that its owner never redeems simply expires.
- On a host using the file vault, every audit record reads the vault for
  redaction. Once the key exists the vault file exists, so each record pays
  one scrypt decryption (work factor 2^15).
