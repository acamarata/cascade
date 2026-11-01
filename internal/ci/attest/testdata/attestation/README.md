# signed_attestation.json

A signed CI attestation record used by `TestAttestGolden`. It was produced by
the real `attest.Attest` path: a fresh Ed25519 node identity generated at test
time, its private key stored in a file-vault custody under a temporary HOME,
and the record signed through `NodeKeystore.Sign`. Only the public key, the
canonical bytes and the signature are kept; the private key was discarded with
the temporary directory.

Regenerate (from the module root):

```
go test -count=1 -run '^TestAttestGolden$' ./internal/ci/attest/ -update-attestation-golden
```

Generated on 2026-10-30 against base commit bc0251c86a935a08b7a393a780e7b41e0c99eacc
with the attestation package added on top. Fields:

- `canonical_json`: the exact canonical record bytes (a JSON string, so the
  bytes survive unchanged).
- `signature`: base64 (no padding) Ed25519 signature over
  `"cascade.ci.attestation.v1\x1f"` followed by the canonical bytes.
- `node_id`, `public_key_b64`: the signing node's identity, checked with
  `nodes.ValidateIdentity` before use.

The test verifies the golden byte for byte, re-encodes it to the same bytes,
and refuses it after flipping any single bit of the canonical bytes.
