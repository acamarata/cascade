# Approval-token fixture provenance

This directory records where the approval-token test fixtures come from, so
a reader can tell what the tests are checking against.

## The external contract

Ed25519 is the external contract here. Every signature in these tests is
produced and checked by the Go standard library's `crypto/ed25519`, never by
a re-implementation of the algorithm in this repository. There is no
hand-rolled Ed25519 anywhere in the tree, and no recorded signature is
verified against anything but the real package.

Two capture paths are used:

- **Round-trip tests** (`approval_token_test.go`) call
  `ed25519.GenerateKey(crypto/rand.Reader)` on every run. The keypair is
  fresh each time, so a passing run means the code agrees with the real
  implementation on a key it has never seen before.
- **The fuzz seed corpus** (`../fuzz/FuzzVerifyToken/`) needs stable bytes,
  so it is signed with a key derived from a fixed 32-byte seed through
  `ed25519.NewKeyFromSeed`. That is still the standard library's own key
  derivation; only the entropy is pinned.

## Capture details

| Item | Value |
| --- | --- |
| Go toolchain | go1.26.6 |
| Platform | darwin/arm64 |
| Capture date | 2026-09-06 |
| Signing algorithm | `crypto/ed25519` (Go standard library) |
| Fixture key seed | the 32-byte ASCII string in `fixtureKeySeed` (`approval_token_test.go`) |
| Fixture issue instant | 2026-03-01T12:00:00Z |
| Fixture expiry | issue + 5 minutes (the §5.24 ceiling) |
| Capture method | `CASCADE_WRITE_APPROVAL_CORPUS=1 go test -run TestWriteApprovalCorpus ./internal/policy/` |

The fixture key seed is a test constant. It signs nothing outside this
directory's fixtures and it is not a secret.

## The seed corpus

Ten named entries, each a deliberate mutation of the same valid token so
that a file's name says exactly what it captures:

| File | What it captures |
| --- | --- |
| `valid-signed.bin` | a well-formed token inside its validity window; the only entry that verifies |
| `truncated.bin` | the first half of a valid token |
| `flipped-signature-byte.bin` | a valid token with the last signature byte flipped |
| `flipped-nonce-byte.bin` | a valid token with one nonce character changed |
| `expired.bin` | a correctly signed token whose expiry has passed |
| `zero-length.bin` | no bytes at all |
| `oversized.bin` | a valid token followed by 70 KB of padding |
| `non-canonical-json.bin` | the canonical payload re-spaced, with a signature over the canonical bytes |
| `unknown-field.bin` | an extra field the record shape does not define |
| `duplicate-key.bin` | the same object key twice |

The last three carry a signature that is valid over the canonical bytes, so
only the strict decoder can be what refuses them. `TestFuzzSeedsAllRefuse`
asserts the outcome of each entry by name.

Entries are in Go's own seed-corpus format (`go test fuzz v1` followed by a
quoted `[]byte(...)` literal), so `go test -fuzz` seeds from them directly.

## Regenerating

Regenerate only when the record shape changes, and say so in the ticket that
changes it. The generator is a skipped test unless its environment variable
is set, so an ordinary run never rewrites the fixtures it is asserting
against.
