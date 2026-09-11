# internal/repo testdata provenance

Every fixture tree under this directory is a small, hand-authored file in
the REAL format each detector's evidence file uses -- never a copy of this
package's own output, and never regenerated from it (Art.2).

- `fixture-go/go.mod` -- a minimal, syntactically real Go module file
  (`module` + `go` directives), the exact shape `golang.org/x/mod/modfile`
  parses in production.
- `fixture-jsts/package.json` + `pnpm-lock.yaml` -- a minimal real npm
  manifest and a pnpm lockfile carrying pnpm's own `lockfileVersion` /
  `importers` top-level keys (the shape pnpm itself writes, abbreviated).
- `fixture-rust/Cargo.toml` -- a minimal real Cargo manifest (`[package]`
  table with `name`/`version`/`edition`, the fields `cargo init` writes).
- `fixture-python/pyproject.toml` -- a minimal real PEP 621 `[project]`
  table.
- `fixture-python-req/requirements.txt` -- a single real pip requirement
  line (`package==version`), the format `pip freeze` emits.
- `fixture-swift/Package.swift` -- a minimal real Swift Package Manager
  manifest, including the `// swift-tools-version:` directive SwiftPM
  itself requires as the file's governing marker.
- `fixture-swift-xcode/App.xcodeproj/project.pbxproj` -- a minimal file
  carrying the exact `// !$*UTF8*$!` header every real Xcode project file
  emits, plus the top-level `archiveVersion`/`objectVersion`/`objects`/
  `rootObject` keys a real `.pbxproj` always has (abbreviated body).
- `fixture-generic/Makefile` -- a real GNU Make file with `build`, `test`,
  and `lint` targets, used to prove the generic fallback family reads
  Makefile targets directly.
- `fixture-override/Makefile` + `.github/workflows/ci.yml` -- a repo with
  no language marker at all, whose Makefile defines `build`/`test`/`lint`
  and whose `.github/workflows/` directory is present, proving the
  "existing CI/Makefile targets override defaults" rule (R-16.37) and CI
  presence detection (`ScanCI`) together against a tree with no
  language-specific evidence.
- `fuzz/FuzzDetectManifest/` -- Go's native fuzz seed-corpus format
  (`go test fuzz v1` + one quoted `[]byte` literal per file), seeded from
  bytes cut from the fixtures above plus a handful of deliberately
  malformed variants (truncated JSON, truncated TOML, a Package.swift with
  no tools-version line). `go test -fuzz` appends its own findings to this
  same directory; none are checked in beyond the seeds this ticket adds.

None of these fixtures, nor any golden derived from them, encodes a host
path, personal name, or machine identifier -- every detector under test
receives a `t.TempDir()` root and every stored fact is repo-relative.

- `fixture-graph-go/` -- a minimal real two-package Go module (`go.mod`,
  `main.go` importing `pkg`, `pkg/foo.go`) exercising the symbol/
  dependency graph extractor (P1-E33-W7-S67-T3) against the real
  `golang.org/x/tools/go/packages` loader, never a hand-built graph. Each
  non-test `.go` file carries a sibling `_test.go` placeholder (mirroring
  `internal/plugins/testdata/conformance/proc_stub`); Go's toolchain
  ignores `testdata/` trees entirely, so these siblings never affect
  extraction. `golden-symbolgraph.json` is the real extractor's own
  canonical-JSON output against this fixture, captured once and checked
  in for the byte-for-byte determinism test -- every `File` path in it is
  relative to the fixture root, never absolute.
- `fixture-graph-malformed/` -- a minimal Go module whose one file has a
  real syntax error, proving the extractor returns a typed error rather
  than a panic or a silently-partial graph.
