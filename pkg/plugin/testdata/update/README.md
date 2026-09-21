# Grant-diff update fixture provenance

`index-bump-with-new-grant.json` is a real, verifiable Ed25519-signed
registry index — one plugin (`grant-diff-demo`), two published versions
(`1.0.0` then `1.1.0`), generated the same way as
`pkg/plugin/testdata/registry/index.json` (X/S-50.T1) and
`pkg/plugin/testdata/tamper/*.json` (X/S-50.T6): the standard library's
`crypto/ed25519`, not this package's own production code.

- Tool: Go standard library `crypto/ed25519` (go1.26.6, see `go.mod` for
  the pinned toolchain version).
- Date generated: 2026-09-21 (index + entries); re-signed 2026-09-21 in the
  S-50.T8 rework pass to carry a real per-version artifact `signature`
  (adversarial CR finding G3/FIX-4 — the original fixture shipped with
  both versions' `signature` fields empty, which an unpatched
  `Ed25519Verifier.VerifyArtifact` treated as "nothing to check" rather
  than refusing; see `verify_artifact.go`'s doc comment for the fix).
- Generation method: a throwaway `go run` program (not committed) built an
  Ed25519 key from a fixed 32-byte seed (bytes `100..131`, distinct from
  both the T1 `index.json` key and the T6 `checksum-mismatch.json` key so
  no test can accidentally cross-verify the wrong fixture). For each
  version entry it signed that version's own artifact bytes directly with
  `ed25519.Sign` (the same bytes `VerifyArtifact` checks against in
  production — `Ed25519Verifier.VerifyArtifact` verifies the signature
  over the raw artifact data, not over the index document), then
  JSON-marshaled the two-version entry, computed the index's own signed
  payload exactly as `Ed25519Verifier.VerifyIndex` reconstructs it
  (`schema_version` plus the raw `entries` bytes, re-encoded via
  `encoding/json`), signed that payload with the same key, and wrote the
  envelope `{schema_version, entries, signature}` to
  `index-bump-with-new-grant.json`.
- Public key (base64, standard encoding):
  `C7w0aldmfDgBIL2cf9flHSxf3+o3zS9b9AWyxr9vLXg=` — embedded directly in
  `update_test.go` (constant `grantDiffFixturePublicKeyB64`) so the fixture
  and its verifying key travel together and a reviewer can re-derive one
  from the other.
- `1.0.0`'s `checksum` is `sha256("grant-diff-demo v1.0.0 artifact bytes
  (not fetched by any test)")` and its `signature` is the real Ed25519
  signature of that same placeholder string under the fixture key — an
  honestly-labeled placeholder artifact, not real installed bytes (no test
  in this ticket ever calls `FetchArtifact` for the CURRENTLY installed
  version — `CheckUpdate` only reports the newer entry — so no real
  "installed artifact" bytes exist to hash or sign), but a placeholder
  that still verifies as a real signature over exactly the bytes it
  claims to cover, matching the checksum computed over those same bytes.
- `1.1.0`'s `checksum` and `signature` are the real SHA-256 digest and
  real Ed25519 signature of the candidate manifest TOML document embedded
  verbatim as the Go constant `grantDiffCandidateManifestTOML` in
  `update_test.go` — the same bytes `update_test.go`'s fake
  `RegistryFetcher.FetchArtifact` returns for this entry's `DownloadURL`,
  so `VerifyArtifact`/`StageAndVerifyArtifact` check a real checksum AND a
  real signature match, not a fabricated one. `plugin.ParseManifest`
  decodes those exact bytes to produce the candidate's `Requires`
  (`["storage.local", "network.egress"]`) that `GrantDiff` compares
  against the installed metadata's `Grants` (`["storage.local"]`) — one
  added capability, `network.egress`, zero removed.

## Why `.json`, not a detached `.minisig` sibling

The ticket names this fixture pair `index-bump-with-new-grant.json` +
`index-bump-with-new-grant.json.minisig`, describing a detached minisign
v2 signature fetched as a second file. `pkg/plugin/testdata/registry/
README.md` (X/S-50.T1) and `pkg/plugin/testdata/tamper/README.md`
(X/S-50.T6) already established, and this ticket's own journal records
again rather than re-deriving, that the real client has no such second
file: `RegistryFetcher.FetchIndex` returns exactly one `[]byte`, and
`RegistryIndex.Signature` is a field embedded in that same JSON document,
not a sibling minisign blob. There is no `index-bump-with-new-grant.json
.minisig` here for the identical, already-ratified reason those two
directories give in full — see either README for the complete rationale.
`Ed25519Verifier` performs real, standard, unforgeable Ed25519 signature
verification (the same primitive minisign itself uses) directly over the
embedded `signature` field.
