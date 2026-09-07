# PBD Contract Lint

`pbd lint` is the `cascade-pbd` builtin plugin's second command
(`plugins/pbd/pbd.go`, mounted alongside T2's `pbd validate`). It answers a
different question than validate does.

- **`pbd validate`** (`plugins/pbd/internal/pews/validate.go`): is the
  PEWS *tree* structurally sound — canonical identity, no duplicate or
  gapped ids, tombstones consistent, dependencies resolvable, no cycles?
- **`pbd lint`** (`plugins/pbd/internal/pews/lint.go`,
  `lint_rules.go`): given a tree that already IS structurally sound, is
  each ticket's own *contract* well-formed and complete enough to build
  from — the 17 fields present and meaningful, dependency forms literal,
  the weight/cr_level floor met, the extra flags used only where
  authorized?

`Lint` never re-derives a fact `Validate` already owns. It composes
`Validate` as its own precondition: a structurally unsound tree is a lint
refusal too, before any contract rule runs.

## Usage

```
pbd lint <tree-root> [phase]
```

- `<tree-root>` is required: the directory containing `epics/`.
- `phase` defaults to `P1` when omitted.
- Exit is clean (no output, exit 0) when every ticket's contract lints
  clean.
- On any issue, the command returns a `*cascade.Error` of kind
  `KindInvalidInput` whose message lists every issue found, each tagged
  with its kind (`[cardinality] ...`, `[art11-missing] ...`) and the
  ticket file's path.

Local check: `go test ./plugins/pbd/... -run '^Test(ContractLint|LintCommand)$'`
(or `./plugins/pbd/internal/pews -run '^TestContractLint'` for the engine
alone).

## Rule map

Every rule below is 06-FORGE-SPEC.md §1/§3-§5 content that is mechanically
checkable from an already-decoded `pews.Ticket` (or, for the three
tree-relative rules, its tree siblings). None of them repeat a check
`pews.Validate` already performs.

| `LintIssueKind` | 06 source | What it checks |
|---|---|---|
| `field-required` | §1 fields 2-5 | `title`/`short_desc`/`full_desc`/`branch` non-empty; `branch` matches the `P1-EX-Wn-Snn-Tn-Kebab-Title` shape |
| `field-too-long` | §1 field 2 | `title` is at most 60 characters |
| `cardinality` | §1 fields 9-10-11-13 | `tasks` has 3-9 entries; `checks`/`acceptance_criteria`/`spec_refs` are non-empty |
| `blank-entry` | §1 | no entry in `tasks`/`checks`/`acceptance_criteria`/`spec_refs` is blank |
| `dependency-form` | §3 | every `depends_on` entry is already a literal canonical ticket id, never a shorthand form (`S-nn.Tn`, a range, an epic letter, `all`) forge should have resolved |
| `cr-weight-mismatch` | §1 field 14, §4 | `cr_level` meets the floor its `weight` implies: XS/S -> CR-A, M/L -> CR-B, XL -> CR-C (a security-class ticket may always add more) |
| `missing-dod-clause` | §5 rule 25 | `acceptance_criteria` carries every Article-3 Ticket DoD phrase |
| `files-scope-empty` | §1 field 12 | `files_scope` declares at least one add/change/delete entry |
| `journals-missing` | §1 extra flags | `journals` is set to `true` |
| `gate-only-unauthorized` / `gate-only-missing` | §3 | `gate_only: true` appears on exactly the closed set §3 names, symmetrically |
| `art11-missing` / `art11-unauthorized` | Art.11, §5 rule 25 | the Article-11 hardening clause appears in `acceptance_criteria` on exactly the TEN tickets Art.11 names (D/S-07.T7, I/S-18.T7, N/S-30.T5, S/S-42.T7, Y/S-52.T7, AF/S-66.T5, AJ/S-72.T6, AM/S-76.T4, AR/S-85.T5, AB/S-58.T7), symmetrically |
| `subticket-dangling` / `subticket-files-overlap` | §1 extra flags | every `subtickets` entry names a real ticket in the tree, and named siblings' `files_scope` entries are pairwise disjoint |

## Deferred (documented, not enforced)

`owner_prereq`'s membership (06 §7's ELEVEN items) is prose-enumerated
over ambiguous epic/sprint ranges ("second machine enrolled", "S
targets") rather than a literal ticket-id list; encoding a closed-set
check for it from that prose risks inventing policy the spec does not
state precisely. `pbd lint` validates `owner_prereq`'s type only (an
optional string) and defers membership enforcement to a future ticket
that can cite an unambiguous table.

## Scope

`pbd lint` mounts only the `lint` command through the `cascade-pbd`
builtin-plugin namespace. Authoring (`create`/`edit`/`move`) is a
separate command set; this ticket adds no core CLI noun, UI, projector,
lifecycle, or dispatch surface.
