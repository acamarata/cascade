# Version scheme

How cascade releases are numbered and tagged.

## Two tags, one release

Every release cuts two git tags that point at the same commit:

| Tag form | What it versions | Governed by |
|---|---|---|
| `v2.Y.Z` | The release artifact: signed binaries, checksums, SBOMs, the Homebrew formula, the OCI image. This is the brand version users and `cascade version` talk about. | The release train, `.goreleaser.yaml` |
| `v0.Y.Z` | The Go module, `github.com/acamarata/cascade`. Go's module system treats `v0.x` as pre-1.0: no import-path major suffix, and Go's own compatibility rules (not this document's) apply until the module crosses 1.0. | `go.mod`, Go's module semver rules |

The two numbers are decoupled on purpose: the module stayed at `v0.x`
(pre-1.0, per the module-path decision that fixed the import path at
`github.com/acamarata/cascade` with no `/v2` suffix) while the product
brand moved straight to `v2` to mark this release as the clean-sheet
rewrite it is. A `v0.Y.Z` tag is never read as a promise about the Go
API's stability; that promise is stated separately in
[compat-promise.md](../api-stability/compat-promise.md), scoped to the
`pkg/` public SDK surface.

Both tags are cut together, at the same commit, by the same release step.
See [release.md](../release.md) for exactly how (the release pipeline's
mechanics, artifact set, and signing flow); this page states the scheme
those mechanics implement, and never repeats them.

## Semver, both sides

`v2.Y.Z` follows semver against the product's own compatibility promises
(this directory's [deprecation-and-abi.md](deprecation-and-abi.md) and
the sibling [api-stability/](../api-stability/) directory): a `Y` bump may
add capability without breaking anything, a `Z` bump is a fix-only patch.
`v0.Y.Z` follows Go's own module-semver rules for a pre-1.0 module, which
is a separate axis with its own meaning under `go get`.

## Alpha and prerelease tags

Wave-gate tickets during this phase's build cut `v2.0.0-alpha.N`
prerelease tags as they land; these are not release cuts and do not carry
the dual-tag pairing above. `self-update --channel stable` resolves to
the latest tag that is not a prerelease; `--channel alpha` includes
prereleases. See `release.md` for the channel matrix in full.

## Install channel

The channel a binary was installed through (script, Homebrew, OCI,
node-managed rollout, or manual) is stamped into the binary at install
time and read by `cascade version` and `self-update`. It is a property of
how a given binary got onto a machine, not of the version scheme itself,
and is documented in full in `release.md`.
