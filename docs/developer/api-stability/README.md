# pkg/ API stability

The public Go SDK surface's stability documentation: what `pkg/` is,
which parts of it are stable, what a downstream consumer may rely on, and
the documentation contract that backs the promise.

- [sdk-surface.md](sdk-surface.md) - what `pkg/` is (`pkg/provider`,
  `pkg/plugin`, `pkg/cascade`) and what it is not (`internal/`, never
  importable by third parties), plus the import-boundary rule stated as
  SDK law.
- [stability-classification.md](stability-classification.md) - the
  stable-vs-experimental classification of every `pkg/` directory that
  exists today.
- [compat-promise.md](compat-promise.md) - what additions and removals
  each require, and the `v0.Y.Z` module-tag caveat stated honestly.
- [godoc-and-examples.md](godoc-and-examples.md) - the godoc-on-every-symbol
  and runnable-example documentation contract.

This directory applies `../versioning/`'s deprecation and ABI policy to
`pkg/`; it does not restate that policy. See `../versioning/README.md` for
the versioning policy set this directory's compat promise depends on.
