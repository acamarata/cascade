# Side-by-side migration protocol

Keep the v1 installation and its data intact while v2 is evaluated. The four
importers cover config, memory, vault, and accounts. Context, fleet,
conversation, plugins, nodes, sync, and backup are not v1 import domains; keep
v1 serving each of those surfaces until v2 is explicitly cleared for it. Do not
dual-write a surface during a cutover.

## Ownership by surface

| surface | v1 writer | v2 writer | stage | cutover proof | rollback owner |
|---|---|---|---|---|---|
| config | v1 config file | v2 config store; v1 importer | Keep v1 authoritative; dry-run, then import | v1 domain ledger done; doctor exit 0 | Operator for config |
| memory | v1 memory files | v2 memory store; v1 importer | Keep v1 authoritative; import then check recall | ledger done and exact recall parity | Operator for memory |
| vault | v1 vault file | v2 secret vault; v1 importer | Preserve v1 source; import with custody available | ledger done; doctor exit 0 | Operator for vault |
| accounts | v1 accounts registry | v2 provider registry; v1 importer | Import; re-authenticate credentials in v2 | migration ledger rows done | Operator for accounts |
| context | none: no imported v1 data; keep v1 serving | v2 context surface | v2-only; stop v2 if not cleared | doctor exit 0; tripwire clean | Operator for context |
| fleet | none: no imported v1 data; keep v1 serving | v2 fleet surface | v2-only; stop v2 if not cleared | doctor exit 0 | Operator for fleet |
| conversation | none: no imported v1 data; keep v1 serving | v2 conversation surface | v2-only; stop v2 if not cleared | doctor exit 0 | Operator for conversation |
| plugins | none: no imported v1 data; keep v1 serving | v2 plugin surface | v2-only; keep v1 active until cleared | doctor exit 0; tripwire clean | Operator for plugins |
| nodes | none: no imported v1 data; keep v1 serving | v2 node surface | v2-only; enroll only after go decision | doctor exit 0 | Operator for nodes |
| sync | none: no imported v1 data; keep v1 serving | v2 sync surface | v2-only; inspect permitted domains before enabling | doctor exit 0 | Operator for sync |
| backup | none: no imported v1 data; keep v1 serving | v2 backup surface | Establish v2 snapshot before v2 writes | doctor exit 0; sweep zero UNRESOLVED | Operator for backup |

The named v1 writer means the existing v1 installation, not a new process or
an instruction to modify its source. The v2 CLI names above are the actual
surface paths. No source-checkout gate promotes a surface by itself.

## Monitoring thresholds

- Monitoring threshold — **doctor exit 0**: run `cascade doctor`; **operator**
  (installed binary).

```console
$ cascade doctor
```

- Monitoring threshold — **migration ledger rows done**: run
  `cascade migrate v1 --from <home> --dry-run` and confirm every domain line
  reads `<domain>: already migrated -- skipped` (the ledger records it as
  done); **operator** (installed binary).

```console
$ cascade migrate v1 --from <home> --dry-run
```

- Monitoring threshold — **S-53.T2 exact recall parity**: run
  `go test -p 4 -count=1 -run '^TestParityChecker_GoldenCoverage$' ./internal/migration/v1`;
  **source-checkout**.

```console
go test -p 4 -count=1 -run '^TestParityChecker_GoldenCoverage$' ./internal/migration/v1
```

- Monitoring threshold — **tripwire clean**: run
  `go test -p 4 -count=1 -run '^TestTripwireStable$' ./internal/migration/tripwire`;
  **source-checkout**.

```console
go test -p 4 -count=1 -run '^TestTripwireStable$' ./internal/migration/tripwire
```

- Monitoring threshold — **sweep zero UNRESOLVED**: run
  `go run -p 4 ./internal/migration/sweep --files <(git ls-files -z) --fail-on-unresolved`;
  **source-checkout**.

```console
go run -p 4 ./internal/migration/sweep --files <(git ls-files -z) --fail-on-unresolved
```

These are the complete threshold set. Use the operator checks from the
installed binary; use the source-checkout checks from the verified repository.
Source-checkout commands run from the repository root in a POSIX shell (bash or
zsh); they are not operator commands.
Do not substitute a time, count, or percentage tolerance.

## Go/no-go

**Go** only when the four imported domains report done in the migration ledger,
the dry-run report has been reviewed, recall parity is exact, the tripwire is
clean, the sweep reports zero UNRESOLVED, and `cascade doctor` exits 0. Record
the operator, source checkout, and the actual outputs before switching the
writer. Keep each non-imported v1 surface serving until its v2 surface has an
explicit cutover decision.

**No-go** if any listed proof is absent, a ledger domain is not done, or a
source-checkout gate fails. Do not retry an ambiguous import against the live
destination; preserve both installations and use the rollback guide.

## Schedule

### Week 1: day-by-day

- Day 1 — inventory the four import sources and confirm the untouched v1
  installation remains available.
- Day 2 — run the v1 importer in dry-run mode; review each domain's report.
- Day 3 — run `cascade backup create` (see the rollback guide), then import the
  approved domains once; record ledger state and operator.
- Day 4 — run the listed operator checks and source-checkout proofs.
- Day 5 — hold v1 and v2 writers steady; resolve any failed proof before a
  cutover decision.
- Day 6 — rehearse the rollback instructions without applying them to live
  state; verify the v1 source remains intact.
- Day 7 — record go/no-go for each imported surface; leave all others on v1.

### Week-by-week after week 1

- Week 1 — complete the day-by-day sequence above; no surface changes writer
  before the no-go list is clear.
- Week 2 — observe the selected v2 writers and repeat the same five thresholds
  from the verified source checkout and installed binary.
- Week 3 — review ledger, recall parity, tripwire, sweep, and doctor evidence;
  retain v1 data and the rollback path.
- Week 4 and each following week — repeat the same evidence review before
  expanding a cutover. A failed proof returns the affected surface to the
  rollback procedure; do not invent replacement thresholds.
