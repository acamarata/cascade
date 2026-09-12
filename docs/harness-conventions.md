# Cross-harness instruction-file conventions

Cascade's context engine (`internal/context`) merges a project's five-tier
instruction cascade (GCI/ASI/PPI/PRI/PAI, see `docs/architecture.md`) and
renders it into the files each supported coding harness reads. This
document is the reference for what gets written, where, and why. The
end-to-end proof that this pipeline works is
`internal/context/roundtrip_test.go`; fixture provenance for that test is
`internal/context/testdata/README.md`.

## 1. Supported harnesses and their files

Three harnesses are supported. Probing the real tools (see
`internal/context/testdata/goldens/`) found that two of the three share one
file format exactly; only the third differs.

| Harness | Per-tier file | Global-tier file | Format |
|---|---|---|---|
| CC | `CLAUDE.md` | `~/.claude/CLAUDE.md` | plain Markdown, taken verbatim |
| codex | `AGENTS.md` | `~/.codex/AGENTS.md` | plain Markdown, taken verbatim |
| opencode | `AGENTS.md` | `~/.config/opencode/AGENTS.md` | plain Markdown, taken verbatim |

None of the three impose a schema on the file: no XML envelope, no
front-matter, no required section order beyond what this document
describes. codex and opencode read the identical file name with identical
semantics, which is why `internal/context/gen_agents.go` renders both from
one shared function rather than two independent serializers that would
agree on the day they were written and drift the first time either changed.

An `XDG_CONFIG_HOME` (opencode) or `CODEX_HOME` (codex) override moves that
tool's global-tier file. Resolving such overrides is the write path's job,
not the generator's; see `internal/context/testdata/README.md` for
the known gap this hands to the sync command.

## 2. Generation invariants

Every generated file obeys three rules, enforced by
`internal/context/gen_harness_conformance_test.go` and
`internal/context/roundtrip_test.go`:

- **Tier ordinal ordering.** Tiers are emitted most general first: GCI,
  then ASI, then PPI, then PRI, then PAI. A harness that only reads part of
  the cascade (see § 1's reach note below) still receives whatever it does
  read in this order.
- **Byte stability.** The same `MergedContext` renders identical bytes on
  every call: no clock, no filesystem read, no map iteration on the output
  path. A generator run twice, or on two different machines, over the same
  merged input produces the same file.
- **No cascade-internal metadata.** The rendered file contains instructions
  a person or a harness can read, never the pipeline's own bookkeeping
  (tier record paths, ordinal numbers, provenance maps). The one exception
  is the managed-block marker itself (see § 3), which is metadata a human
  reader needs in order to know what cascade owns.

A harness's own directory walk decides how much of the cascade it actually
reads. codex and opencode were both captured reading the global tier plus
everything from the git root down to the working directory, but NOT the
tiers above the git root (ASI, and PPI when distinct from ASI). The
generator still renders every tier that contributed content; which tiers a
given harness's own file-discovery walk will find is a property of that
harness, not of the generator, and is recorded per-harness in
`internal/context/testdata/README.md`.

## 3. The managed-block contract and byte stability

Cascade never owns an instruction file outright. Each tier's file may also
carry a human's own hand-written prose, before the managed block, after it,
or (for a harness like CC that predates cascade on a given machine) with no
managed block at all. `internal/context/harness_gen.go` is the whole of
this contract:

- A managed block is delimited by an HTML-comment marker pair
  (`<!-- cascade:generate-instructions digest=sha256:... -->` ...
  `<!-- /cascade:generate-instructions -->`). The opening marker carries a
  digest of the block's own body.
- On a later write, cascade recomputes that digest against the bytes
  actually on disk. A match means nobody touched the block since cascade
  wrote it, and it is replaced. A mismatch means a human edited it, and the
  active `WritePolicy` decides: `RefuseIfEdited` (default) stops the write
  and returns a typed conflict; `BackupIfEdited` preserves the previous
  file at `<name>.cascade-bak` before overwriting.
- Content OUTSIDE the marker pair, before it or after it, is never read for
  this comparison and never rewritten. A file with no marker at all is
  hand-authored and is appended to, never truncated.

This is why the round-trip fixture's hand-edit tests are the load-bearing
ones: a test that only checked the managed block's own content would pass
while silently destroying a user's own text around it, which is the
failure this contract exists to prevent.

## 4. The no-Gemini-harness ruling

Gemini is a model-provider lane (accessed through Cascade's provider
routing, not through an instruction-file convention) and is never a fourth
entry in the table above. This is a ratified project decision, not an
oversight: Gemini has no equivalent to a `CLAUDE.md`/`AGENTS.md` convention
to generate for, and adding one would misrepresent it as a coding harness
in the sense this document uses the word.

## 5. Format-pin policy and upgrade procedure

Each harness's file name, global-tier location and verbatim-Markdown
contract are pinned against a captured version of that harness's own
binary (recorded in `internal/context/testdata/README.md`, with
the exact version and capture command). When a harness ships a new major
version:

1. Re-run the same capture command against the new binary.
2. Diff the new capture against the committed one. A file-name or
   location change is a breaking change to this document and to
   `internal/context/gen_agents.go` / `gen_cc.go`; a captured field that
   simply gained new data (as happened between two CC point releases in
   `internal/context/testdata/transcripts/README.md`) is additive and does
   not require a generator change.
3. Update the pinned version and capture in
   `internal/context/testdata/README.md`, and add a test case if
   the new capture demonstrates behaviour the existing suite does not
   cover.

Never change a generator's target file name or format on the strength of
documentation alone; the captured evidence is the thing that must move
first.

## 6. Extending Cascade for a new harness

A new harness is one more implementation of the `HarnessGenerator`
interface in `internal/context/harness_gen.go`:

```go
type HarnessGenerator interface {
    Generate(mc MergedContext) ([]HarnessFile, error)
}
```

To add one:

1. Capture the real tool's own file-discovery and file-format behaviour
   first (a `debug`-style command if the tool has one, or its shipped
   source, per the examples in
   `internal/context/testdata/README.md`). Do not invent a format.
2. Implement `Generate`, reusing `renderTierBlock` and the other shared
   helpers in `internal/context/gen_cc_sections.go` if the new harness's
   format turns out to be the same verbatim-Markdown dialect codex and
   opencode already share; only write a new renderer if the capture shows
   a genuinely different format.
3. Register the writer alongside the other three so
   `gen_harness_conformance_test.go`'s cross-writer suite covers it
   automatically: ordering, idempotency, hand-edit refusal, and the
   content-agreement check that a user switching harnesses never silently
   switches instructions.
4. Add the new harness to the table in § 1 of this document and to the
   provenance index in `internal/context/testdata/README.md`.

## 7. Bulk regen across many projects

`cascade context sync --project-list FILE` scans many project directories
at once instead of just the working directory, reusing the exact
generation and write path described above for every project it finds
drift in. See `docs/migration-guide.md` for the flag's write semantics
(`--check`, `CASCADE_NO_INPUT`, `--yes`).
