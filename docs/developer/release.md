# Release pipeline

This is the developer-facing walkthrough of cascade's release train
(P1-E01-W1-S01-T6, Epic A's acceptance ticket). It covers the pipeline
layout, the artifact set, how to verify a release locally, minisign
user-verification instructions, env-ref key handling, and what publishes
when. See `.goreleaser.yaml` and `.github/workflows/release.yml` for the
executable source of truth — this document explains the *why*, not a
duplicate of the config.

## Pipeline layout

| File | Owns |
|---|---|
| `.goreleaser.yaml` | Build matrix, archives, checksums, SBOMs, minisign signing, Homebrew cask definition. |
| `internal/buildinfo/buildinfo.go` | The single ldflags-stamp source of truth (version, commit, date, install channel). |
| `.github/workflows/release.yml` | CI verification lane: `goreleaser check`, `goreleaser build --snapshot`, and (when the minisign owner-prereq secrets exist) a full signed `goreleaser release --snapshot`. |
| `docs/THIRD_PARTY_LICENSES` | Third-party license report for every module actually compiled into a release binary, across all three release GOOSes. |

## Release matrix

Exactly five platforms (R-14.1): `darwin/arm64`, `darwin/amd64`,
`linux/amd64`, `linux/arm64`, `windows/amd64`. Every binary is a static build
(`CGO_ENABLED=0` — core has no CGO, 06-FORGE-SPEC §2) with `-trimpath` and a
commit-timestamp module mtime for reproducibility.

## The version stamp (§D-33)

`internal/buildinfo` is the **only** ldflags injection point (T0 ruling
R-14.116). `.goreleaser.yaml` stamps it:

```
-X github.com/acamarata/cascade/internal/buildinfo.Version={{ .Version }}
-X github.com/acamarata/cascade/internal/buildinfo.Commit={{ .Commit }}
-X github.com/acamarata/cascade/internal/buildinfo.Date={{ .Date }}
-X github.com/acamarata/cascade/internal/buildinfo.InstallChannel=script
```

`cmd/cascade/version.go` (`cascade version`) reads these symbols and prints
them; it declares no stamp variables of its own. A plain `go build` with no
`-ldflags` yields the documented dev defaults (`dev` / `none` / `unknown`,
channel `manual`) rather than silently emitting empty strings.

**Install channel is currently stamped `script`** for every artifact this
pipeline produces (the direct-download binaries `install.sh`, once it exists,
would fetch). The other §D-33 channel values (`brew`, `oci`, `node-managed`)
are read-side-ready (`buildinfo.ResolvedInstallChannel()` accepts all five),
but nothing in this ticket differentiates the *same compiled bytes* by
distribution channel — that is deferred to the tickets that own each channel
(AA/S-55.T4 for brew, a future OCI entrypoint for `oci`, the controller for
`node-managed`).

## Artifacts produced by a snapshot release

Running `goreleaser release --snapshot --clean` locally produces, per
platform:

- an archive (`.tar.gz` on darwin/linux, `.zip` on windows) containing the
  binary, `README.md`, `LICENSE`, and `THIRD_PARTY_LICENSES`
- a syft SBOM (`<archive>.sbom.json`, SPDX JSON) — a **real** `syft` binary
  invocation, never a hand-authored SBOM dialect (Art.2)
- one shared `cascade_<version>_checksums.txt` (sha256) over every archive
- one minisign signature over the checksums file
  (`cascade_<version>_checksums.txt.minisig`)
- a rendered (not published) Homebrew cask definition under
  `dist/homebrew/Casks/cascade.rb`

Nothing publishes. `goreleaser` skips GitHub Release creation, the Homebrew
tap push (`skip_upload: auto`), and any registry push automatically under
`--snapshot`; `release.draft: true` is a second safety net for the rare case
a real (non-snapshot) run happens without a human present to click "Publish".

## Verifying a snapshot release locally

```sh
goreleaser check                          # validates .goreleaser.yaml
goreleaser build --snapshot --clean       # cross-compiles all 5 platforms, no signing

# Real minisign sign+verify cycle (ephemeral keypair, never committed):
mkdir -p .minisign-e2e
minisign -G -W -s .minisign-e2e/verify-e2e.key -p .minisign-e2e/verify-e2e.pub
CASCADE_MINISIGN_KEY=.minisign-e2e/verify-e2e.key \
CASCADE_MINISIGN_PUBKEY=.minisign-e2e/verify-e2e.pub \
  goreleaser release --snapshot --clean

(cd dist && shasum -a 256 -c *_checksums.txt)
minisign -Vm dist/*_checksums.txt -p .minisign-e2e/verify-e2e.pub

rm -rf dist .minisign-e2e   # never leave ephemeral key material or build output around
```

`.minisign-e2e/` is gitignored — it must never exist outside a throwaway
local check.

## minisign user-verification instructions (end users)

Every published release's checksums file carries a minisign signature from
cascade's escrowed release key. To verify a downloaded archive:

```sh
# 1. Get cascade's public key (published out-of-band per Art.8.8 — NOT from
#    this repo's history, so a compromised repo can't also forge the key).
# 2. Verify the checksums file itself:
minisign -Vm cascade_<version>_checksums.txt -p cascade-release.pub
# 3. Verify your downloaded archive matches a checksum line:
shasum -a 256 -c cascade_<version>_checksums.txt
```

If either check fails, do not run the binary.

## Key handling (never in tracked files)

- `CASCADE_MINISIGN_KEY` / `CASCADE_MINISIGN_PUBKEY` are env-refs to files
  **outside** the repository. `.goreleaser.yaml`'s `signs:` block reads them
  via `{{ .Env.CASCADE_MINISIGN_KEY }}`; no key material is ever templated
  into a tracked file.
- In CI (`.github/workflows/release.yml`), the real escrowed key lives in the
  `CASCADE_MINISIGN_KEY`/`CASCADE_MINISIGN_PUBKEY` repo secrets, written to a
  `${RUNNER_TEMP}` file for the duration of one job step and deleted
  immediately after (`if: always()`).
- cosign KEYLESS signing needs GitHub OIDC (`id-token: write`) and therefore
  only ever runs CI-side — it is never invoked from a local snapshot, and it
  is not yet wired into this repo's CI at all (see **Known gaps** below).

## What publishes when

| Stage | Trigger | Publishes? |
|---|---|---|
| Local snapshot (`goreleaser release --snapshot`) | developer machine | Never. |
| CI `verify` job | every push | Never — `goreleaser build` only, no signing, no secrets. |
| CI `snapshot` job | push to `main`/`p1-integration`, or manual dispatch | Never — still `--snapshot`; only runs signing when the minisign owner-prereq secrets exist, and only uploads a *workflow artifact* (14-day retention), not a GitHub Release. |
| Wave alpha tags (`v2.0.0-alpha.N`) | AA/S-55/S-56 tickets | Signed binaries + checksums from this same train, per 09-REVIEW-RESOLUTIONS.md §Round 7. |
| rc cut + real publish | AA/S-55.T4 | The first ticket that flips real publishing on: GitHub Release, Homebrew tap push, OCI push + cosign attestation. |
| v2.0.0 final | AA/S-56.T6 | The gated, owner-countersigned final publish. |

No ticket before AA/S-55.T4 performs a real publish of any kind — every
green run before then is a **local or CI-side verification**, not a release.

## Known gaps (owner action / T0 ruling required)

- **OCI server-profile image.** This ticket's `files_scope` has no path for
  a `Dockerfile`, and 12-QUALITY-CONSTITUTION.md Art.10.1's clean-root
  allowlist does not list `Dockerfile` among the permitted root files
  either. `.goreleaser.yaml` therefore has no `dockers:` section and
  `.github/workflows/release.yml` has no cosign/OCI step — building either
  without a real image to build/sign would be a simulated capability
  (Art.1). This is tracked as a cross-ticket seam pending a T0 ruling on
  where the Dockerfile lives (see this ticket's journal).
- **Escrowed minisign keypair.** The real owner-prereq keypair (06-FORGE-SPEC
  §7) has not been generated/escrowed yet. Until it is, and the
  `CASCADE_MINISIGN_KEY`/`CASCADE_MINISIGN_PUBKEY` repo secrets are set, the
  CI `snapshot` job's signing step skips itself cleanly (see its `::notice::`
  output) rather than failing red.
