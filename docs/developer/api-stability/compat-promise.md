# SDK compat promise

What a downstream consumer of `pkg/provider`, `pkg/plugin`, or
`pkg/cascade` may rely on across releases.

## Additions

Adding a new exported symbol (a function, type, method, or constant) to a
stable `pkg/` package needs a documentation entry: an update to
[stability-classification.md](stability-classification.md) if it is a new
package, and godoc on the new symbol per
[godoc-and-examples.md](godoc-and-examples.md). An addition never breaks
an existing consumer by itself; that is exactly the property that makes
it an addition rather than a breaking change.

## Removals and behavior breaks

Removing an exported symbol, or changing its behavior in a way an
existing caller could observe, needs the one-minor deprecation window
`../versioning/deprecation-and-abi.md` defines: the symbol is marked
deprecated for one full minor release before it is actually removed. This
document applies that window to `pkg/`; it does not restate the window's
mechanics, which live entirely on that page.

The same page's `cascade.plugin/v2` manifest ABI promise is the plugin
manifest's own, stronger version of this rule (stable across the whole
v2.x line, no deprecation cycle needed because there is no in-place
breaking change to that schema at all). This document's compat promise
covers the Go SDK types in `pkg/provider` and `pkg/plugin`; the manifest
schema's promise is the sibling rule on the versioning page, not
duplicated here.

## The v0.Y.Z module-tag caveat, stated honestly

The Go module itself is tagged `v0.Y.Z` (pre-1.0, per the module-path
decision fixing the import path at `github.com/acamarata/cascade` with no
version suffix). Go's own module-semver rules apply to a pre-1.0 module
independently of the promise on this page: Go tooling does not enforce
`v0.x` compatibility the way it would for a `v1+` module.

Stated plainly: this document's compat promise is a project commitment
backed by the one-minor deprecation cycle and enforced by review, not a
guarantee `go get` or `go mod tidy` can verify on the module's own
version number. The `v2.x` brand promise this page and
`../versioning/version-scheme.md` describe is real and is what this
project holds itself to; it is a human and process promise layered on top
of a module path that Go's tooling still sees as pre-1.0.
