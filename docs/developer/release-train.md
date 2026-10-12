# Release train: the publish half

This page covers how a release candidate's bits are built, where they go
before anyone signs them, and which steps stay gated. The executable sources
are `.goreleaser.yaml` and `.github/workflows/release.yml`; the shape gates in
`internal/build/releaseconfig_test.go` fail the build when either drifts from
what this page describes.

## The train

| Stage | Where | What it produces | Signed | Public |
|---|---|---|---|---|
| Snapshot | CI on every push (`snapshot`, `install-e2e`) | Archives, SBOMs, checksums, OCI image | Ephemeral key made in the job | No (nothing uploaded) |
| rc-publish | CI, manual dispatch only | `build-<id>`: final-version bits in the private staging package | No | No |
| Local signing | The build Mac, at the rc cut | `dist-<id>`: the signed payload | Release key (offline) | No |
| Promotion | CI, manual dispatch | GitHub release, tap commit, image tags, attestations | Yes, over the promoted bytes | Yes |

`<id>` is the first 12 hex characters of the built commit.

## What a build contains

- Two executables for every platform (darwin and linux on amd64 and arm64,
  windows on amd64): `cascade` and its GitHub plugin `cascade-github`
  (`cascade-github.exe` on windows). Both are static (`CGO_ENABLED=0`), built
  with `-trimpath`, and both run the devkeys guard
  (`go run ./internal/build/nodevkeys`) after each build.
- Archives `cascade_<version>_<os>_<arch>.tar.gz` (`.zip` on windows) holding
  both executables, `README.md`, `LICENSE` and `THIRD_PARTY_LICENSES`.
- One syft SBOM per archive: `<archive>.sbom.json`.
- `cascade_checksums.txt`: sha256 of every archive, every SBOM and
  `install.sh`. The name carries no version, so `install.sh` and the promote
  job fetch it by one fixed name.
- `cascade_checksums.txt.minisig`: minisign signature whose trusted comment is
  exactly `cascade <version> checksums`. `install.sh` refuses a signature whose
  version does not match the archive it installs, so an older signed release
  cannot be served under a newer tag.
- `Casks/cascade.rb` for the `acamarata/homebrew-cascade` tap. It installs both
  binaries, downloads from the public `acamarata/cascade` releases, and its
  caveats point at the public key
  (`https://acamarata.github.io/cascade/keys/minisign.pub`, also
  `docs/keys/minisign.pub`). The key is never inlined in the cask.
- The OCI image `ghcr.io/acamarata/cascade` (linux/amd64 and linux/arm64) with
  `/usr/local/bin/cascade` and `/usr/local/bin/cascade-github`.

## The install channel

Release binaries are unstamped: no ldflag sets the install channel, so
`cascade version` prints `channel: manual` for any downloaded binary. The
channel lives in the installer's receipt instead (`install.sh` writes
`"channel":"script"` to `~/.cascade/install-receipt.json`). One binary can
then ship through every channel unchanged. The `verify` job and the
`install-e2e` job both assert `channel: manual` on the built binary.

## rc-publish

Dispatch `release.yml` with these two inputs and leave `record_json` empty. A set `record_json` runs the promote job instead (`docs/developer/promotion.md`):

| Input | Format | Meaning |
|---|---|---|
| `final_tag` | `v2.Y.Z` | The version the bits are stamped with. There is no rc input: the bits that pass the rc are the bits that ship. |
| `commit_sha` | 40 lowercase hex | The commit to build. |

The job:

1. Refuses before checkout when the `CASCADE_STAGING_REMOTE` repository secret
   is empty or not a lowercase `<owner>/<repo>`, or when an input is
   malformed. It prints which check failed, never a value.
2. Checks out `commit_sha` and refuses unless it is the commit the run was
   dispatched on (the tip of the chosen branch, or the commit of the chosen
   tag), so the bits come from exactly the release config this run executes.
   Dispatch again if the branch moved.
3. Creates `final_tag` in the runner only and runs
   `GORELEASER_CURRENT_TAG=<final_tag> goreleaser release --clean
   --skip=publish,sign`. The stamp is the final version even when an rc tag
   points at the same commit. A local check of the same bits uses this exact
   command in a throwaway clone with a local tag; `--snapshot` builds always
   carry a `-SNAPSHOT-<commit>` suffix and never produce final-version names.
4. Pushes the two images as `build-<id>-amd64` and `build-<id>-arm64`, joins
   them into the multi-arch index `build-<id>-index`, and writes their
   digests to `dist/cascade_images.txt` (lines `<sha256:digest>  <name>` for
   `cascade-linux-amd64`, `cascade-linux-arm64` and the index `cascade`).
5. Pushes `build-<id>` with oras: archives, SBOMs, `cascade_checksums.txt`,
   `cascade_images.txt`, `install.sh` and `Casks/cascade.rb`.

Its permissions are exactly `contents: read` and `packages: write`. The
staging remote comes only from the secret (the runner masks it); the
workflow has no `vars.` reference and no `::add-mask::`. Push output goes to
a file that stays in the runner, so no digest, artifact list or registry
reference reaches the public run log. goreleaser's own log names the
final-version archives, which is accepted. The job never signs, attests,
uploads a workflow artifact, uses a cache, runs `git push`, creates a release
or commits to the tap.

Every other job sets `CASCADE_STAGING_REMOTE=example-owner/example-repo`:
`release.github` is templated from that variable, so goreleaser cannot
template a release target without it and a publish can never fall back to a
default repository. Run `goreleaser check` locally the same way:

```sh
CASCADE_STAGING_REMOTE=example-owner/example-repo goreleaser check
```

## The local signing step

The rc cut pulls `build-<id>` to the build Mac and assembles `dist-<id>`,
which holds exactly:

- every file listed in `cascade_checksums.txt`, and that file itself;
- `cascade_images.txt`, added as a line of `cascade_checksums.txt`, so the
  image digests are covered by the signature;
- `cascade_checksums.txt.minisig`, made with the offline release key and the
  trusted comment `cascade <version> checksums` (`<version>` is `final_tag`
  without the `v`);
- `Casks/cascade.rb`.

The checksums file is extended first and signed last. No CI secret holds a
release signing key; promotion consumes only `dist-<id>` and signs or copies
images only by the digests in the signed `cascade_images.txt`.

## Dual tags

Each release has an artifact tag and a Go module tag. The binary is
`v2.Y.Z`; the module stays on the `v0` line. `buildinfo.ModuleTagFor` maps
one to the other and refuses anything else:

| Artifact tag | Module tag |
|---|---|
| `v2.0.0` | `v0.0.0` |
| `v2.0.0-rc.1` | `v0.0.0-rc.1` |
| `v2.3.4` | `v0.3.4` |
| `v3.0.0`, `v2.0`, `2.0.0` | refused (invalid input) |

## What stays gated

Nothing in this train publishes on a push. Tags, GitHub releases, tap
commits, public image tags, cosign signatures and provenance attestations
are all made at promotion, by a manual dispatch, over bytes that already
carry the offline signature.
