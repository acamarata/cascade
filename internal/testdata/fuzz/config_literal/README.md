# internal/testdata/fuzz/config_literal — FuzzTomlLiteral + FuzzTomlEdit seed corpus

Seed corpus for `internal/runtime.FuzzTomlLiteral` and
`internal/runtime.FuzzTomlEdit` (`internal/runtime/fuzz_test.go`), per
06-FORGE-SPEC.md §5.7 (fuzz corpora live under `internal/testdata/fuzz/`,
never beside the owning package — see the sibling `manifest/` directory
for the pattern this one follows) and this ticket's
(P1-E03-W1-S05-T8) `files_scope`, which names this exact path.

Two files, one per fuzz target, matching the two-parser split this ticket
implements (config_write.go's TOML-literal value parser vs
toml_edit.go/toml_edit_scanner.go's structure-preserving line editor):

- `literal_seeds.txt` — one TOML-literal value string per line, read by
  `FuzzTomlLiteral`. Covers every scalar shape `ParseTomlLiteral` accepts
  (bool, int in decimal/hex/octal/binary, float including exponent form,
  basic and literal strings, arrays) plus two shapes it must reject
  cleanly (a bare TOML date/datetime, which is a valid TOML literal but
  not one of the five scalar kinds 08-INIT-CONFIG-SPEC.md §3 lists for
  `cascade config set`) — included so the fuzzer starts from a corpus that
  already exercises both the accept and the reject path.
- `toml_edit_seeds.txt` — one small TOML document fragment per line, read
  by `FuzzTomlEdit` as candidate `src` inputs (each combined with a fixed
  `dotted="a.b"`/`literal="1"` pair by `fuzz_test.go`, which is what
  provides key/value variety in this fuzz target — the file's job is
  document-shape variety). Covers a table header, a nested-table header,
  a plain key=value, a dotted key=value, an inline-commented key=value, a
  comment-only line, an array-of-tables header (which SetKeyLine/
  UnsetKeyLine must never mis-treat as a settable scalar table — see
  `parseTableHeader`'s doc comment), and two lines with `=` inside a
  quoted value (proving `findTopLevelEquals` is exercised against exactly
  the case it exists for).

Provenance (Article 2): both files are hand-authored directly from
08-INIT-CONFIG-SPEC.md §3's own literal-value examples (`true`, `42`,
`1.5`, `"s"`, `["a","b"]`) plus the TOML v1.0.0 spec's scalar-value
grammar (https://toml.io/en/v1.0.0, accessed 2026-09-02) for the
additional int/float/string forms — not harvested from any external
corpus or tool. Tool: none (manually written). Date: 2026-09-02.
