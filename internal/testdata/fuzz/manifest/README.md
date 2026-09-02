# internal/testdata/fuzz/manifest — FuzzParseManifest seed corpus

Seed corpus for `pkg/plugin.FuzzParseManifest` (`pkg/plugin/fuzz_test.go`),
per 06-FORGE-SPEC.md §5.7 (fuzz corpora live under `internal/testdata/fuzz/`,
never beside the owning package) and this ticket's (P1-E03-W1-S05-T6)
`files_scope`, which names this exact path.

The three `.toml` files here are byte-identical copies of the golden
manifest fixtures in `pkg/plugin/testdata/` — see
`pkg/plugin/testdata/README.md` for full provenance (tool: spec-derived;
version: 02-TARGET-STRUCTURE.md §First-party plugin catalog v1; date:
2026-08-31). `fuzz_test.go` reads every `.toml` file in this directory at
`FuzzParseManifest`'s setup time and seeds each one via `f.Add`, so the
fuzzer starts every run already exercising three real, schema-valid
manifests rather than an empty corpus.

This directory is named `manifest` (not `FuzzParseManifest`) because the
ticket contract's `files_scope` names it exactly that way; it is a
hand-curated seed set read explicitly by `fuzz_test.go`; the location gate
in `.github/workflows/fuzz-nightly.yml` (`fuzz-corpus-location` job) only
asserts that every `*/testdata/fuzz` tree lives under
`internal/testdata/fuzz/`, not any particular subdirectory-naming
convention, so this satisfies the gate. It is distinct from the
`testdata/fuzz/FuzzParseManifest/` directory Go's own `go test -fuzz`
tooling would create next to the package for crash/interesting-input
persistence — no such directory is created or used by this ticket.
