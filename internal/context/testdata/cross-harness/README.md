# cross-harness: the conformance corpus

Goldens captured from real runs of the three shipped instruction
generators, one file per harness. `internal/context/cross_harness_test.go`
asserts against them; `TestCrossHarnessGoldenCorpus` re-captures them.

## Provenance

| | |
|---|---|
| tool | cascade's own three `HarnessGenerator` implementations (`CCInstructionWriter`, `CXInstructionWriter`, `OCInstructionWriter`) |
| version | the tree at the ticket's commit |
| date | 2026-09-16 |
| ticket | P1-E16-W4-S35-T4 |
| capture command | `go test ./internal/context/ -run TestCrossHarness -update-cross-harness` |
| fixture | a two-tier project: a global tier `CASCADE.md` under a fake home, a repo tier `CASCADE.md` under a `t.TempDir()` project. No absolute paths reach the output. |

### What kind of fixture this is, precisely

These are **regression goldens over our own output**, not external-
counterpart fixtures. Art.2's real-counterpart rule is about formats
somebody else owns, and nothing here is captured from another tool. The
external counterparts for this package live elsewhere in this tree and are
labelled as such: `goldens/cx/*.capture.txt`, `roundtrip/codex/AGENTS.md`,
and `cc-harness-fixtures/harness-config.json`. Calling a self-capture an
Art.2 counterpart would be the exact self-dialect problem Art.2 exists to
prevent, so this file says plainly which it is.

What the corpus is genuinely for: an unintended change to the instruction
format fails `TestCrossHarnessGoldenCorpus` with a readable diff. That is
worth the committed bytes. Re-capture is deliberate and reviewed, never
automatic.

### Why there is no `hash.golden`

The ticket's layout asks for a separate per-harness hash file. There is
none, on purpose: the managed block's digest is already inside
`instruction.golden`, in the opening marker, where the code puts it. A
second copy of a value that already exists in the artifact beside it can
only ever do one thing, which is disagree with it.

## Layout

```
claude/instruction.golden      all files CCInstructionWriter renders
codex/instruction.golden       all files CXInstructionWriter renders
opencode/instruction.golden    all files OCInstructionWriter renders
```

Each golden carries every file its writer produced, each preceded by a
`=== role=<tier> name=<path>` header. The header names the tier by slug
rather than by the enum's integer, so a role rename shows up in a diff
instead of silently renumbering.

## The fact these goldens make visible

Read the three side by side and one thing stands out: at the repo tier,
`codex` and `opencode` both render `AGENTS.md`, byte for byte identical,
at the same path. One file on disk serves both harnesses. They only
diverge at the global tier (`.codex/AGENTS.md` versus
`.config/opencode/AGENTS.md`).

That is correct — `AGENTS.md` is a convention those tools share — but it
has consequences that are easy to get wrong, and two of them were:

- the drift check reports the shared file once, so a consumer keying by
  harness found no entry for the second one and reported it in sync; see
  `DriftResult.AlsoServes` and `TestCrossHarnessPathIsolation`
- uninstalling either harness deletes the file the other still reads; see
  `internal/plugins/testdata/cross-harness/README.md`
