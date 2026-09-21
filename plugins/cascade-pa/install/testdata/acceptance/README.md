# Acceptance fixture provenance — P1-E24-W5-S50-T7

`index.json` is a real, verifiable Ed25519-signed registry index for the
Epic X acceptance story ("link my GitHub" → cascade-github). It is signed
the same way `pkg/plugin/testdata/registry/index.json` is (see that
directory's own `README.md`), using a **different** key so the two
fixtures never share signing material.

## index.json — what `pkg/plugin.Ed25519Verifier` actually verifies

- Tool: Go standard library `crypto/ed25519` (no CGO, no third-party
  crypto), invoked by a throwaway `go run` program (not committed) —
  `gen_fixture.go`, run once from the module root on 2026-09-21.
- Key: an Ed25519 key derived from the fixed 32-byte seed
  `"P1-E24-W5-S50-T7-acceptance-fix2"[:32]` via
  `ed25519.NewKeyFromSeed`. Public key (base64, standard encoding),
  embedded in `acceptance_x_test.go` as `acceptancePublicKeyB64` so the
  fixture and its verifying key travel together:
  `14rm+t9vx2h0KKOhATbZmLDlzwLAhDBKiz+rKKUoJwM=`
- Entry: one `cascade-github` entry, `latest_version "0.1.0"`, tags
  `["link my github", "github", "vcs"]` (the "link my github" tag is what
  `internal/plugins/resolver`'s exact-tag tier matches the acceptance
  test's intent string against — see `resolver.go`'s `registryTier`).
- Artifact checksum + signature: the version entry's `checksum` is the
  hex SHA-256 of the REAL `plugins/github/manifest.toml` bytes (Y/S-51.T1's
  actual shipped manifest, read directly from the repo tree — not copied
  into this directory, so it can never drift from what Y/S-51.T1 ships;
  `download_url` is documented (`file://plugins/github/manifest.toml`) but
  never fetched — the acceptance test reads the same on-disk file the URL
  names, matching Art.7 §2's "zero network calls" requirement) at
  generation time, hex-encoded. `signature` is `ed25519.Sign` over those
  same bytes under the same fixture key. Both are checked by the acceptance
  test's installer harness through `pkg/plugin.Ed25519Verifier.VerifyArtifact`
  — the same production Ed25519 verification `internal/plugins`'s real
  `installerAdapter` performs (see `cascadepa_install_verify.go`).
- Index signature: `ed25519.Sign` over `{schema_version, entries}`,
  re-encoded exactly as `Ed25519Verifier.VerifyIndex` reconstructs it
  (`pkg/plugin/registry_verify.go`) — the SAME scheme, and the SAME
  documented contract-vs-tree resolution `pkg/plugin/testdata/registry/
  README.md` already recorded: `RegistryIndex.Signature` is a field
  embedded in the JSON document itself, not a detached minisign
  signature over a second fetched file. This embedded signature is what
  the acceptance test's real `plugin.NewVerifiedIndex` call actually
  verifies.

**Drift note:** the checksum above is pinned to `plugins/github/manifest.toml`
as it existed on 2026-09-21 (sha256
`53f2a071b7d73e8e613fdbf99c435284461878e3ee9adbb359983d6414a5a418`). If a
later ticket edits that manifest, the acceptance test's checksum-verify
step will start failing — deliberately: it is a real-counterpart proof
against the actual shipped artifact, not a private copy, so it also
functions as a drift detector (the same pattern
`cmd/cascade/plugin_process_mount.go`'s
`TestProcessPluginSpecsMatchTheShippedManifest` uses for the same file).
Regenerate with `gen_fixture.go`'s technique (a fixed-seed Ed25519 key,
never re-derived per run) if that ever happens.

## index.json.minisig — real minisign CLI provenance

`files_scope` names `index.json.minisig` as a sibling fixture file. As
`pkg/plugin/testdata/registry/README.md` already records for the T1
registry client: the real, shipped `pkg/plugin.Ed25519Verifier` has no
method to fetch or check a second, detached signature file —
`RegistryFetcher.FetchIndex` returns exactly one `[]byte`, and the
signature it checks lives inside that same document (see above). This
ticket's acceptance test therefore does not read `index.json.minisig` at
all; production code never consumes it in this tree.

It is still produced with the real, installed `minisign` CLI (verified
`minisign 0.12` at `/opt/homebrew/bin/minisign`) rather than a
self-authored dialect, satisfying the ticket's "never a self-authored
minisign dialect" requirement for the file this name implies:

```
minisign -G -W -p acceptance.pub -s acceptance.sec        # throwaway keypair, 2026-09-21
minisign -S -s acceptance.sec -m index.json -x index.json.minisig \
  -t "P1-E24-W5-S50-T7 acceptance fixture index.json"
minisign -V -p acceptance.pub -m index.json -x index.json.minisig  # verified locally, rc=0
```

Minisign public key used (throwaway, not reused anywhere else):
`RWSyHJzhYyPhykyDA0hHD32HT+matFRxJLHNZGvelVW/01fwz51uLioR`. The signing
key is not retained.
