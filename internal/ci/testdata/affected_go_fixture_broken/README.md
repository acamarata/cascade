# affected_go_fixture_broken

Provenance:

- tool: `go list`
- go version: the toolchain running the test (`runtime.Version()`)
- date: 2026-09-22
- purpose: real-counterpart proof that an unrelated, untouched package's
  build failure elsewhere in the tree makes `affectedGoTargets` fail
  CLOSED to `TargetAll`, never a raw error (P1-E32-W6-S65-T1, D1 REWORK
  round 2 fix, confirm review case 3). A separate module from
  `affected_go_fixture/` so this permanent breakage never affects that
  fixture's own (healthy) tests.

Layout:

- `pkg/healthy`: a real, independently-buildable Go package. The test
  changes `pkg/healthy/healthy.go`.
- `pkg/broken`: holds `broken.go` (a normal, trivial Go file) plus
  `extra.c`, an orphan cgo-less C source file. A `.c` file with NO `.go`
  file alongside it is silently skipped by `go list ./...` (verified
  empirically, go1.26.2 darwin/arm64 -- not itself a bug this fixture
  covers); the real failure mode needs at least one real `.go` file in
  the same directory, which is what makes `go list` treat the directory
  as a genuine package and then refuse it outright ("C source files not
  allowed when not using cgo or SWIG"), breaking the whole-tree
  import-graph subprocess even though nothing under `pkg/broken`
  changed.
