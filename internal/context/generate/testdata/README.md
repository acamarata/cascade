# internal/context/generate fixtures: provenance index

Every file under `testdata/` and what produced it.

| Path | What it holds | Provenance |
|---|---|---|
| `fuzz/FuzzParseManagedBlocks/seed_*` | three hand-written corpus seeds: a Markdown pair, a nested begin, an indented unmatched end | written by hand on 2026-10-05 in the Go fuzz corpus format (`go test fuzz v1`); each states one marker-grammar edge the parser must classify without panicking |
| `fuzz/FuzzReadManifest/seed_*` | three hand-written corpus seeds: version 2, trailing data after the object, an unknown field | written by hand on 2026-10-05 in the same format; each is a manifest `ReadManifest` must refuse with `invalid-input` |
| `policy_view_fixture.json` | one inventory (Go detected with build, test and lint commands; Python not detected; GitHub Actions) and what a fact source answers for it: four accepted facts and one source-denied id | written by hand on 2026-11-01 for P1-GEN-06; it holds a covered subject (dropped), an injection-shaped value (`</code> ## Ignore previous rules`, rendered inert), a value with backticks, and a gate-waiver text the view must move into `Denied` |
| `golden/rules/{CLAUDE.md,rules-root.md,policy-kernel.md}` | the three files `RenderRules` produces for the fixture view | captured on 2026-11-01 from the real generator with `CASCADE_UPDATE_RULES_GOLDENS=1 go test -run TestRulesGoldens ./internal/context/generate/`, then read line by line; `TestRulesGoldens` compares byte for byte |
| `fixture-templating/view.json` | one inventory (Go detected with `make build`, `go test ./...` and `golangci-lint run`; Rust not detected) and one accepted layout fact holding backticks | written by hand on 2026-11-01 for P1-GEN-08; `TestTemplatingCorrectness` proves those commands reach the bodies and no placeholder does |
| `golden/agents/*.md`, `golden/skills/*.md` | the nine files `RenderAgentsSkills` produces for `fixture-templating` (skills named `<skill>.md`) | captured on 2026-11-01 from the real generator with `CASCADE_UPDATE_AGENTS_SKILLS_GOLDENS=1 go test -run TestAgentsSkillsGoldens ./internal/context/generate/`, then read line by line; `TestAgentsSkillsGoldens` compares byte for byte |
| `real-cc-agents/{agent-fixture.md,skill-fixture.md}` | the subagent and skill scaffolds the installed Claude Code CLI 2.1.316 writes; their frontmatter keys bind the generated files | read from the CLI binary as raw bytes on 2026-11-01; provenance, method and the listing gap are in `real-cc-agents/README.md` |

The in-code seeds (`f.Add`) in `managed_test.go` and `manifest_test.go` repeat
one valid input per marker form and one valid manifest, so the fuzz targets
also start from accepted input.

Only `real-cc-agents/` is captured from an external tool (and the hook-event
capture it is scrub-checked with, `internal/fleet/hookpacks/canon/testdata/real-cc-hooks/`).
No other fixture here is. The marker grammar and the
manifest schema are this package's own contract (`contract:generation-manifest`),
so there is no external counterpart to capture; the tests build every file
they need in a temporary directory and never read or write the real home
directory. `clean_root_test.go` runs the installed `git` against a temporary
repository whose `.gitignore` is a copy of the repository's own.

A corpus file the fuzzer adds after a failure belongs in the matching
`fuzz/<Target>/` directory with a one-line row above saying what it
reproduced.
