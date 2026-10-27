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

### Assembling dist-<id>

`.github/scripts/assemble-rc-dist.sh` does the assembly on the build Mac. It
reads `CASCADE_BUILD_DIR` (the pulled `build-<id>`), `CASCADE_DIST_OUT` (must
not exist), `CASCADE_APPLE_SIGNING_IDENTITY` (`-` for ad-hoc),
`CASCADE_NOTARY_SKIP`, `CASCADE_MINISIGN_KEY` (a key file; minisign reads its
passphrase from the script's stdin, never from argv or the environment) and
`CASCADE_RELEASE_VERSION`. With no argument it runs `sign`, `submit` and
`finish` in one call; real notarization can be resumed with `finish` (exit 75
while Apple still reports In Progress). Ad-hoc signing (`-`) and
`CASCADE_NOTARY_SKIP=1` are refused unless the version is a `-SNAPSHOT-`
rehearsal. `$CASCADE_DIST_OUT.lock` lets one assembly at a time work on an
output. Run it from a clean clone at the release commit: the helper must come
from the commit the darwin `cascade` was built from.

1. **sign**: copy the build input into a private work directory
   (`$CASCADE_DIST_OUT.work`, mode 0700) and check every copied file against
   the copied `cascade_checksums.txt` before anything else. Each darwin
   archive must hold exactly `cascade`, `cascade-github`, `README.md`,
   `LICENSE` and `THIRD_PARTY_LICENSES`, and both darwin `cascade` binaries
   must name the same `vcs.revision` and `vcs.time` with `vcs.modified=false`
   (a `metadata.json`, when present, must name that commit). Build both
   helpers with `build-elevate-helper.sh` (cgo, `-trimpath`, without the
   caller's `GOFLAGS`, `GOEXPERIMENT`, cgo flags or git location overrides
   such as `GIT_DIR`; refused unless its own tree is a clean git checkout at
   that commit with a real `.git`, the go env file sets no build or module
   source setting, and
   `go version -m` shows `CGO_ENABLED=1`, darwin, the arch, no `-tags`, the
   same `vcs.revision` and `vcs.modified=false`, and the devkeys guard
   passes), place each beside
   `cascade`, and codesign the helper, `cascade-github` and `cascade` with the
   hardened runtime, a secure timestamp and their own identifiers
   (`cascade-elevate-helper`, `cascade-github`, `cascade`).
   `apps/elevate-helper/entitlements.plist` goes on the helper only. Every
   signature must pass `codesign --verify --strict` with its identifier.
2. **submit**: zip the six signed executables and notarize them through
   `notary-lib.sh` (intent line, then the submission id, in the journal).
3. **finish**: after Accepted (or a skip-mode submit), re-pack each darwin
   archive deterministically (C-sorted names, the commit time (`vcs.time` of
   the verified `cascade`) as every mtime, uid and gid 0, no extended
   attributes, `gzip -n`). The packed archive is extracted again and every executable
   must equal the signed bytes and still verify. The SBOMs are regenerated
   with the pinned syft (the version `release.yml` installs). The darwin
   lines of `cascade_checksums.txt` and the cask's darwin `sha256` fields are
   rewritten, the cask installs `cascade-elevate-helper` on macOS, the
   `cascade_images.txt` line is added and minisign signs the checksums with
   the trusted comment `cascade <version> checksums`.

Linux and windows archives, their SBOMs and `install.sh` are copied
byte-identical and carry no helper. `CASCADE_DIST_OUT` is created only by the
final step, which refuses when anything appeared there meanwhile, so a
refusal leaves nothing there; a failed `sign` also removes the work
directory. The passphrase descriptor is closed for the helper build (go, clang), codesign,
ditto, notarytool and syft; only minisign reads it. The scripts never write to a keychain, never trace
(`set -x`) and print `notarytool` output only through the redaction filter.
A snapshot rehearsal (`goreleaser release --snapshot`) has no
`cascade_images.txt`; any other version without it is refused. Stage a
snapshot `dist/` the way rc-publish does (`cp install.sh dist/` and
`dist/homebrew/Casks/cascade.rb` to `dist/Casks/cascade.rb`) before using it
as `CASCADE_BUILD_DIR`.

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
