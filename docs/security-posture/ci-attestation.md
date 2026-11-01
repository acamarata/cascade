# CI attestations

A CI attestation is a node's signed statement that an acceptance re-run of
one job ran on one exact commit and tree, under one policy, with a given
result. The receive gate trusts a commit only through these records, so this
page is the contract every verifier relies on.

## Record fields (schema version 1)

The record carries, in this canonical key order:

`artifact_hashes`, `attestor_class`, `checkpoint_id`, `commit_sha`,
`ended_at`, `environment`, `exit_status`, `expires_at`, `job_spec_hash`,
`key_id`, `node_id`, `nonce`, `policy_version`, `repo_id`, `result_hashes`,
`schema_version`, `started_at`, `target_selection`, `toolchain_fp`,
`tree_hash`, `oidc_token_sha256`, `oidc_run_id`.

- The list is versioned and additive: a later version only appends names. A
  record with a key its `schema_version` does not list, or a
  `schema_version` above the one the verifier supports, is refused.
- `policy_version`, `checkpoint_id`, `nonce`, `expires_at`, `commit_sha`,
  `tree_hash`, `node_id` and `key_id` are required. A record missing any of
  them cannot be built or verified.
- `result_hashes` entries are `<plan kind>:<digest>`. `environment` holds
  `ephemeral`, `isolation_class`, `node_id`, `runner_kind` and
  `toolchain_fp`.
- The two `oidc_*` fields are empty for node-built records.

## Canonical bytes and signature

- Keys in the order above, no whitespace, times as RFC 3339 UTC with whole
  seconds, hash arrays sorted, absent arrays as `[]`.
- A verifier decodes the record only from these bytes and refuses any input
  that does not re-encode to exactly the same bytes (other key order,
  spacing, duplicate keys, case variants, unsorted arrays).
- The signature is Ed25519 by the node identity key over
  `cascade.ci.attestation.v1` followed by the byte `0x1f` and the canonical
  bytes. The same key also signs heartbeats, rotations and ssh user
  authentication; the prefix keeps any of those from passing as a record.
- There is no second signing key: the node identity key signs, through its
  custody, and the private key never leaves custody. The controller never
  signs on a node's behalf.
- A stored row's id is the hex SHA-256 of the canonical bytes. Every
  stored column that mirrors a signed field must equal the signed value; a
  verifier re-checks this on every row it reads. The `attestor_class`
  column is the controller's derived class, checked only to be a class name;
  eligibility is always derived again, never read from it.
- The bytes are Go `encoding/json` output: `<`, `>` and `&` are written as
  `\u003c`, `\u003e` and `\u0026`, and U+2028/U+2029 are escaped. A
  signer in another language must produce the same escapes.

## Attestor classes

The class is derived by the verifier, never read from the record:

| Class | When |
|---|---|
| `enrolled-node` | both oidc fields empty and the signature verifies under the node's current key |
| `ephemeral-oidc` | both oidc fields set and the controller's countersignature verifies |
| `unattested` | evidence with no attesting signer |
| `unverified` | anything else, including an unknown or empty class name |

A record whose own `attestor_class` disagrees with the derived class is
`unverified`. Only `enrolled-node` and `ephemeral-oidc` can satisfy a gate.
The zero class is `unverified`.

## Trust stores and rotation

- The record store resolves a node to its current key only, and only for a
  node whose trust tier is worker-trusted or controller; a paired device
  never attests. A key id is the
  first 16 hex characters of SHA-256 of the public key. Any other key id of
  a known node, and a revoked current key, are refused; an unknown node is
  refused.
- Rotation supersedes the old key at once, with no overlap window, so a
  record signed by a superseded key no longer verifies.
- The controller store answers for the controller's own identity, with the
  same one-key rule, and passes every other node to the record store. When
  the controller is also enrolled as a node, a rotation or revocation in the
  record store refuses its key here too.

## Expiry and binding

- A record is refused at or after `expires_at`, when `expires_at` is more
  than 24 hours after `started_at`, and when `started_at` is more than five
  minutes ahead of the verifier's clock (so a future-dated record cannot
  stretch its window).
- A record verifies only for the commit and tree the caller resolved: the
  tree comes from the commit itself (with git replacement refs ignored),
  never from the record. Either one differing refuses.
- Each acceptance re-run gets a single-use nonce (128 random bits, at most 24
  hours). The controller binds the repo id, job spec hash, policy version
  and plan kinds to it when it is issued. Consuming it is part of the
  accepting transaction; a second use or an expired nonce is refused.
  Verification only reads nonces and never consumes one.
- A record counts only when it is passing: exit status 0 and a result hash
  for every kind of the accepted plan. Its repo id, job spec hash and policy
  version must equal the accepted fact's values (the expected values, never
  the record compared with itself), and it must have been accepted between
  its `started_at` and `expires_at`.
