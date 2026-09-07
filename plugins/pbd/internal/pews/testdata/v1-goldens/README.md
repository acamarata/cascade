# v1-goldens: archived-validator behavioral fixtures

`validator-cases.json` records the OBSERVED BEHAVIOR of the archived structural
checker `.claude/planning/p1/.plan-audit.py` — the tool 06-FORGE-SPEC.md §8 and
12-QUALITY-CONSTITUTION.md §8 name as authoritative until this ticket's engine
exists ("the forge validator (N/S-28.T2) supersedes it once the engine exists").
That tool lives in the gitignored planning corpus, not the tracked repo, so this
directory harvests its input/output BEHAVIOR only — never its source.

## Provenance

- Source path: `.claude/planning/p1/.plan-audit.py`
- Source MD5 (harvest time): `630ffa77ac0d86d941742e783b8eb02e`
- Harvest date: 2026-09-06
- Harvested by: P1-E14-W3-S28-T2

## What was harvested and why

The archived tool parses PEWS structure out of Markdown plan prose (a ticket
grammar like `EP/S-nn.Tn`, and inline `Tn TOMBSTONED` markers), not YAML
files. This ticket's engine (`internal/pews`) validates the canonical YAML
tree directly, so the fixtures below translate the archived tool's four
structural findings into this engine's tree/violation vocabulary rather than
reusing its literal input format:

| Archived finding | This engine's violation kind |
|---|---|
| `DUPLICATE <id>` | `duplicate-id` |
| `<ep>/<sprint> gap at T[...]` | `gap` |
| `Tn TOMBSTONED` marker suppressing a gap | a matching `tombstones.yaml` entry suppressing `gap` |
| per-epic declared-vs-parsed count mismatch | `Report.ActiveCount`/`Report.TombstoneCount` |

`TestArchivedValidatorFixtureParity` (`plugins/pbd/validate_test.go`) builds a
synthetic tree per case and asserts the native validator produces exactly the
recorded `expect_violation_kinds`.

## Rule

Never copy v1 (or archived-tool) implementation code into this tree. Only
recorded input/output behavior, with stated provenance, belongs here.
