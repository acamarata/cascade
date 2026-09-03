# Release pipeline

This is the developer-facing walkthrough of cascade's release train
(P1-E01-W1-S01-T6, Epic A's acceptance ticket). It covers the pipeline
layout, the artifact set, how to verify a release locally, minisign
user-verification instructions, env-ref key handling, and what publishes
when. See `.goreleaser.yaml` and `.github/workflows/release.yml` for the
executable source of truth: this document explains the *why*, not a
duplicate of the config.

## Pipeline layout

| File | Owns |
|---|---|
| `.goreleaser.yaml` | Build matrix, archives, checksums, SBOMs, minisign signing, Homebrew cask definition. |
| `internal/buildinfo/buildinfo.go` | The single ldflags-stamp source of truth (version, commit, date, install channel). |
| `.github/workflows/release.yml` | CI verification lane: `goreleaser check`, `goreleaser build --snapshot`, and (when the minisign owner-prereq secrets exist) a full signed `goreleaser release --snapshot`. |
| `.github/docker/Dockerfile` | OCI server-profile image definition (R-14.119, not at repo root; Art.10.1's clean-root allowlist has no root-Dockerfile entry, and `.github/` is where Art.10.1 already re-homes CI-adjacent infrastructure). |
| `docs/THIRD_PARTY_LICENSES` | Third-party license report for every module actually compiled into a release binary, across all three release GOOSes. |

## Release matrix

Exactly five platforms (R-14.1): `darwin/arm64`, `darwin/amd64`,
`linux/amd64`, `linux/arm64`, `windows/amd64`. Every binary is a static build
(`CGO_ENABLED=0`, core has no CGO, 06-FORGE-SPEC §2) with `-trimpath` and a
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
distribution channel; that is deferred to the tickets that own each channel
(AA/S-55.T4 for brew, a future OCI entrypoint for `oci`, the controller for
`node-managed`).

## Artifacts produced by a snapshot release

Running `goreleaser release --snapshot --clean` locally produces, per
platform:

- an archive (`.tar.gz` on darwin/linux, `.zip` on windows) containing the
  binary, `README.md`, `LICENSE`, and `THIRD_PARTY_LICENSES`
- a syft SBOM (`<archive>.sbom.json`, SPDX JSON): a **real** `syft` binary
  invocation, never a hand-authored SBOM dialect (Art.2)
- one shared `cascade_<version>_checksums.txt` (sha256) over every archive
- one minisign signature over the checksums file
  (`cascade_<version>_checksums.txt.minisig`)
- a rendered (not published) Homebrew cask definition under
  `dist/homebrew/Casks/cascade.rb`
- two OCI server-profile images (`ghcr.io/acamarata/cascade:<version>-amd64`
  and `...-arm64`, `linux/amd64` + `linux/arm64`, the R-14.1 matrix's linux
  legs), built locally in the Docker daemon and never pushed

Nothing publishes. `goreleaser` skips GitHub Release creation, the Homebrew
tap push (`skip_upload: auto`), and any registry push automatically under
`--snapshot`; `release.draft: true` is a second safety net for the rare case
a real (non-snapshot) run happens without a human present to click "Publish".

## OCI server-profile image

`.github/docker/Dockerfile` builds a minimal image that runs the **same
statically linked `cascade` binary** the linux archives ship: "server
profile" (02-TARGET-STRUCTURE.md "Profiles": Postgres/pgvector/S3/Redis vs
local SQLite/fs) is a runtime config selection, not a separate build, so
there is exactly one binary and one image definition.

- **Base:** `gcr.io/distroless/static-debian12:nonroot`: no shell, no
  package manager, nothing to pivot to. Runs as uid/gid `65532`
  (`nonroot`); no `USER` directive is needed because the base already sets
  it, so a future base-image swap that silently reverted to root would be
  visible in the Dockerfile diff rather than hidden behind an easy-to-miss
  line.
- **Entrypoint:** `/usr/local/bin/cascade`: the image runs `cascade` and
  nothing else. Server-profile config (DB/S3/Redis credentials, etc.) is
  supplied entirely via runtime env/config-file mounts, never baked in.
- **Platforms:** `linux/amd64` and `linux/arm64`, combined into one
  multi-arch manifest (`docker_manifests:` in `.goreleaser.yaml`) tagged
  both `<version>` and `latest`.
- **Location ruling:** R-14.119: the Dockerfile lives at
  `.github/docker/Dockerfile`, not repo root.

Locally verified (`goreleaser release --snapshot --clean`, per-arch images
built by Docker Desktop's buildx, run directly):

```sh
$ docker run --rm ghcr.io/acamarata/cascade:<snapshot>-arm64 version
cascade version 0.0.0-SNAPSHOT-<commit>
commit:  <commit>
built:   <date>
channel: script

$ docker run --rm ghcr.io/acamarata/cascade:<snapshot>-arm64 --help
Cascade is a local-first AI agent runtime: ...
```

Registry push (`ghcr.io`) and cosign keyless attestation (§D-16, needs
GitHub OIDC) are deferred to AA/S-55.T4; neither is wired anywhere yet, CI
included. A local snapshot never pushes anything; `skip_push: auto` on both
the per-arch images and the manifest is a second safety net alongside
`--snapshot`'s implicit `--skip=publish`.

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

`.minisign-e2e/` is gitignored: it must never exist outside a throwaway
local check.

## minisign user-verification instructions (end users)

Every published release's checksums file carries a minisign signature from
cascade's escrowed release key. To verify a downloaded archive:

```sh
# 1. Get cascade's public key (published out-of-band per Art.8.8, NOT from
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
  only ever runs CI-side: it is never invoked from a local snapshot, and it
  is not yet wired into this repo's CI at all (see **Known gaps** below).

## What publishes when

| Stage | Trigger | Publishes? |
|---|---|---|
| Local snapshot (`goreleaser release --snapshot`) | developer machine | Never. |
| CI `verify` job | every push | Never: `goreleaser build` only, no signing, no secrets. |
| CI `snapshot` job | push to `main`/`p1-integration`, or manual dispatch | Never: still `--snapshot`; only runs signing when the minisign owner-prereq secrets exist, and only uploads a *workflow artifact* (14-day retention), not a GitHub Release. |
| Wave alpha tags (`v2.0.0-alpha.N`) | AA/S-55/S-56 tickets | Signed binaries + checksums from this same train, per 09-REVIEW-RESOLUTIONS.md §Round 7. |
| rc cut + real publish | AA/S-55.T4 | The first ticket that flips real publishing on: GitHub Release, Homebrew tap push, OCI push + cosign attestation. |
| v2.0.0 final | AA/S-56.T6 | The gated, owner-countersigned final publish. |

No ticket before AA/S-55.T4 performs a real publish of any kind; every
green run before then is a **local or CI-side verification**, not a release.

## Known gaps (owner action / T0 ruling required)

- **OCI registry push + cosign attestation.** R-14.119 resolved where the
  Dockerfile lives (`.github/docker/Dockerfile`) and the image now builds
  for real, both locally and in CI's `snapshot` job; see **OCI
  server-profile image** above. What remains deferred to AA/S-55.T4 is the
  actual `ghcr.io` push and the cosign KEYLESS attestation over the pushed
  image: wiring a real push now, with no owning ticket ready to consume it
  and no publish authorized yet (06-FORGE-SPEC §7: any publish is GATED),
  would be exactly the kind of unfinished-but-live publish surface Art.1
  forbids. `id-token: write` in `.github/workflows/release.yml` is reserved
  for that step and unused today.
- **Escrowed minisign keypair.** The real owner-prereq keypair (06-FORGE-SPEC
  §7) has not been generated/escrowed yet. Until it is, and the
  `CASCADE_MINISIGN_KEY`/`CASCADE_MINISIGN_PUBKEY` repo secrets are set, the
  CI `snapshot` job's signing step skips itself cleanly (see its `::notice::`
  output) rather than failing red.
