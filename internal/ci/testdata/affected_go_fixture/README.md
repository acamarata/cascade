# affected_go_fixture

Provenance:

- tool: `go list`
- go version: the toolchain running the test (`runtime.Version()`); no fixed
  version is pinned since the fixture exercises `go list`'s own import-graph
  output, which is stable across supported Go versions
- date: 2026-09-03
- purpose: real-counterpart affected-target test (P1-E32-W6-S65-T1). This is
  a real, independently-buildable Go module (own `go.mod`), excluded from
  the parent `github.com/acamarata/cascade` module by the `testdata/`
  directory-name convention `go build`/`go list` apply automatically. It is
  not a hand-authored graph the production code merely echoes: `pkg/b`
  really imports `pkg/a`, and `internal/ci/affected_go_test.go` invokes the
  real `go` binary against this tree to prove `RequirementModel.Affected`
  reads its output rather than a fixture-shaped stand-in.

Layout:

- `pkg/a`: a leaf package, no imports. `pkg/a/testdata/sample.txt` is a
  non-buildable data file nested under a `testdata/` directory, proving a
  changed path there maps to the nearest enclosing package (`pkg/a`)
  rather than being dropped or forcing a full fallback.
- `pkg/b`: imports `pkg/a` -- the one real production reverse-dependency
  edge the fixture's cross-cutting-vs-isolated tests walk.
  `pkg/b/asset.txt` is a non-Go file living directly inside the package
  directory (an embed-source/`.s`-file stand-in), proving a changed
  non-Go path inside a package directory maps to that package.
  `pkg/b/b_test.go` (an in-package test file) imports `pkg/c` -- `pkg/c`
  has no OTHER importer, proving the reverse-dependency walk follows
  `_test.go` import edges (`.TestImports`/`.XTestImports`), not just
  ordinary production `.Imports`.
- `pkg/c`: a leaf package imported only by `pkg/b`'s `_test.go` file, per
  above -- never by any production `.go` file.
