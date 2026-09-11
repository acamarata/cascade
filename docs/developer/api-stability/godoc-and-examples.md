# Godoc and examples

The `pkg/` SDK's documentation contract.

## Every exported symbol carries godoc

Every exported symbol under `pkg/provider`, `pkg/plugin`, and
`pkg/cascade` has a godoc comment. This is not a style preference here:
it is the contract this SDK is documented against, and it is CI-checked
so a missing comment on a new exported symbol fails the build rather than
shipping undocumented.

## Runnable examples on entry points

Where an exported symbol is an entry point - a constructor, a top-level
function a caller starts from, or an interface a caller implements from
scratch - it carries a runnable Go `Example` function alongside its
godoc, not only prose. `pkg/provider` already ships this pattern:
`example_blob_test.go`, `example_cache_test.go`, `example_queue_test.go`,
`example_store_test.go`, and `example_vector_test.go` are runnable
examples for the interfaces in that package, and `pkg/plugin` has its own
`example_test.go`. A new entry point follows the same pattern: an example
that compiles and runs as part of the normal test suite, not a code
fence in a markdown file that can silently drift from the real API.

## Enforcement

This document states the contract. The CI gate that enforces it (failing
the build when an exported `pkg/` symbol has no godoc, or when an entry
point has no runnable example) is a separate ticket's work; this page is
documentation for the promise, not the enforcement code itself.

## Cross-reference: the manifest side

The `cascade.plugin/v2` manifest schema's own stability promise (stable
across the entire v2.x line) is documented in
`../versioning/deprecation-and-abi.md`, not here. This page's godoc and
example contract covers the Go SDK types a plugin author's *code*
touches; the manifest is a data schema a plugin author's *manifest file*
conforms to, and its stability rule is stated once, on the versioning
page, not duplicated on this one.
