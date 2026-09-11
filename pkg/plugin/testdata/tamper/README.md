# Tamper fixture provenance

Every fixture here is derived from the real Ed25519-signed fixture at
`pkg/plugin/testdata/registry/index.json` (X/S-50.T1; see that directory's
own README for its provenance) via scripted byte manipulation, per
Art.2 §2 — none is a self-authored protocol variant.

- Tool: Go standard library `crypto/ed25519` and `encoding/json` (same
  toolchain pinned in `go.mod`), driven by a throwaway `go run` script
  (not committed).
- Date generated: 2026-09-11.

## File naming: `.json`, not `.minisig`

The ticket names these fixtures `sig-truncated.minisig`,
`sig-wrong-body.minisig`, `empty.minisig`, matching its minisign-detached-
signature assumption. X/S-50.T1 established (and this ticket's own journal
records) that the real client has no detached signature file at all —
`RegistryIndex.Signature` is one field inside the single JSON document
`RegistryFetcher.FetchIndex` returns. Every fixture here is therefore a
full `index.json`-shaped document with a `.json` extension; there is no
second file to truncate or substitute independently of the body.

## Fixtures

- **`sig-truncated.json`** — the real fixture with its `signature` field's
  base64 value truncated by exactly one trailing character. Entries
  unchanged.
- **`sig-wrong-key-body.json`** — the real fixture's entries (unchanged)
  with `signature` replaced by a signature that IS validly computed, but
  by a different Ed25519 key over a different body (the private key and
  body backing `checksum-mismatch.json`, below — not a byte-garbled
  string). Verifying it against the real fixture's public key
  (`testRegistryPublicKeyB64` in `registry_client_test.go`) must fail: it
  is a genuine wrong-key/wrong-body signature, not corrupted bytes.
- **`body-modified-after-sign.json`** — the real fixture with one
  character changed inside `entries` (`"Example Formatter"` ->
  `"Example Formattes"`) after the real signature was computed; the
  signature field is left untouched, so it no longer matches the payload
  it is checked against.
- **`malformed.json`** — deliberately invalid JSON (an unterminated
  object), not derived from the real fixture's bytes since a JSON-parse
  failure is a shape distinct from any signature mutation.
- **`empty-signature.json`** — the real fixture with `signature` set to
  `""`. (Ticket names this `empty.minisig`, implying an empty detached
  signature FILE; the real client has no such file, so the equivalent
  tamper is an empty signature FIELD in the one document it does fetch.)
- **`checksum-mismatch.json`** — a small, freshly Ed25519-signed index
  (fresh test-only key, seed bytes 200..231; public key
  `MrU+iC49qsGA16X2Ik1htkAbQ/kB2wnw5NhM5OSicYs=`, base64 standard
  encoding) with one entry whose `checksum` is a value that does not
  match any real artifact. Its own signature verifies correctly against
  its own public key (`Ed25519Verifier.VerifyIndex` accepts it); the
  point of this fixture is that index-signature validity and per-entry
  checksum correctness are independent properties —
  `plugin.VerifyArtifact` on this entry must reject it despite the index
  itself verifying.
