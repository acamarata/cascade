# internal/context/generate fixtures: provenance index

Every file under `testdata/` and what produced it.

| Path | What it holds | Provenance |
|---|---|---|
| `fuzz/FuzzParseManagedBlocks/seed_*` | three hand-written corpus seeds: a Markdown pair, a nested begin, an indented unmatched end | written by hand on 2026-10-05 in the Go fuzz corpus format (`go test fuzz v1`); each states one marker-grammar edge the parser must classify without panicking |
| `fuzz/FuzzReadManifest/seed_*` | three hand-written corpus seeds: version 2, trailing data after the object, an unknown field | written by hand on 2026-10-05 in the same format; each is a manifest `ReadManifest` must refuse with `invalid-input` |

The in-code seeds (`f.Add`) in `managed_test.go` and `manifest_test.go` repeat
one valid input per marker form and one valid manifest, so the fuzz targets
also start from accepted input.

No fixture here is captured from an external tool. The marker grammar and the
manifest schema are this package's own contract (`contract:generation-manifest`),
so there is no external counterpart to capture; the tests build every file
they need in a temporary directory and never read or write the real home
directory. `clean_root_test.go` runs the installed `git` against a temporary
repository whose `.gitignore` is a copy of the repository's own.

A corpus file the fuzzer adds after a failure belongs in the matching
`fuzz/<Target>/` directory with a one-line row above saying what it
reproduced.
