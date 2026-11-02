# Node attest signing

Who signs what when a controller asks an enrolled node to attest a CI run.

## Who signs what

| Signature | Key | Signs |
|---|---|---|
| Request | Controller installation key (`installationPublicKey`, signed through `productionEnvelopeSigner`) | The `ci.attest.sign` request payload |
| Record | Node identity key (`NodeKeystore.Sign`) | The attestation record the node built itself |

The controller never signs a record on a node's behalf. The controller's
per-node identity key never signs a request, and a request signed with it
is refused.

## The binding pin

`cascade node enroll --controller-endpoint <url>` prints
`{endpoint, enrollment_id, controller_pubkey_b64}`. The operator runs
`cascade node bind --binding-file <path>` on the node. Bind is elevated
(`node.bind`), names the key id of the key it pins, and writes
`<data_dir>/nodes/controller_binding.json` with exclusive create (0600,
parent 0700, fsynced). There is no check-then-write window.

An existing binding is never replaced. The same key is a no-op, a different
key is `CONFLICT`, an unreadable key is `INVALID_INPUT`. Changing the pin
means re-provisioning the node. The binding never comes from an RPC field.

## The request

Payload: `cascade.ci.attest.sign.v1`, then node id, execution id, nonce,
expires-at (RFC 3339), checkpoint id, repo id, job spec hash and policy
version, each preceded by the 0x1F separator. A field containing 0x1F is
refused, so two different field tuples never share a payload.

## Refusals (all `PERMISSION_DENIED` unless noted)

- No pin on the node, a wrong key, or a bad signature.
- `ExpiresAt` in the past.
- Any field the node does not define (strict decode); results such as exit
  status, hashes or tree are never accepted from the caller.
- An execution id with no completed run in the node's own store.
- A second sign of the same run (`CONFLICT`).

The node copies repo id, job spec hash and policy version from the signed
request, and derives exit status, result hashes and the tree hash from its
own run. Altering a fact field after the controller signed breaks the
signature.
