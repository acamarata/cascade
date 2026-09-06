# PBD ticket schema v2

The cascade-pbd plugin's PEWS ticket-schema v2 is the typed contract model
and YAML codec for a forged ticket. It is implemented in
`plugins/pbd/internal/pews` and defines the shape of a ticket document —
nothing about how a ticket tree is persisted, authored, dispatched, or
projected.

## The 17 fields

A ticket carries exactly 17 fields, in this normative order:

| # | Field | Type | Notes |
|---|---|---|---|
| 1 | `id` | string | |
| 2 | `title` | string | |
| 3 | `short_desc` | string | one sentence |
| 4 | `full_desc` | string | usually a YAML block literal (`\|`) |
| 5 | `branch` | string | |
| 6 | `weight` | enum | `XS`, `S`, `M`, `L`, `XL` |
| 7 | `model_class` | enum | `mech`, `build`, `heavy`, `review`, `arbiter` |
| 8 | `depends_on` | []string | ordered; may be `[]` |
| 9 | `tasks` | []string | ordered |
| 10 | `checks` | []string | ordered; each a verbatim shell command |
| 11 | `acceptance_criteria` | []string | ordered |
| 12 | `files_scope` | mapping | see below |
| 13 | `spec_refs` | []string | ordered |
| 14 | `cr_level` | enum | see below |
| 15 | `qa_level` | enum | `QA-A`, `QA-B`, `QA-C` |
| 16 | `sport_updates` | []string | ordered |
| 17 | `docs_updates` | []string | ordered |

Every ordered list field keeps the order the YAML document declared, both on
decode and on a subsequent encode.

## Extra flags

Beyond the 17 fields, exactly five extra flags are declared and no others:

| Flag | Type | Notes |
|---|---|---|
| `subtickets` | []string | present only when files_scope is disjoint across parallel agents |
| `journals` | bool | always `true` when present |
| `owner_prereq` | string | one of the owner-prerequisite items |
| `gate_only` | bool | marks an owner-prereq-gated acceptance ticket |
| `external_contract` | bool | marks a ticket touching an Article-2 external contract |

Every extra flag is optional, and the codec preserves the distinction
between "omitted" and "present with its zero value": an omitted `journals`
key decodes to a nil pointer, not `false`; an explicit `journals: false`
decodes to a non-nil pointer to `false`. `subtickets` uses the same
omitted-vs-empty distinction via a nil vs. empty slice.

This package enforces no rule about WHERE an extra flag may apply — that
validation belongs to a later PBD ticket.

## `files_scope` shape

```yaml
files_scope:
  add:
  - path/to/new_file.go
  change:
  - path/to/existing_file.go
  delete: []
```

`add`, `change`, and `delete` are each an ordered list of repository-relative
paths. Any list may be empty (`[]`) or omitted; no other key is permitted
under `files_scope`.

## `cr_level` value set

`cr_level` is a non-empty, `+`-joined, strictly increasing combination of
`CR-A`, `CR-B`, and `CR-C` — for example `CR-B`, `CR-A+CR-B`, or
`CR-A+CR-B+CR-C`. The join order always follows `CR-A`, `CR-B`, `CR-C`; a
different order, a repeated token, or an unrecognized token is invalid.

## Decoding and errors

`DecodeTicket([]byte) (Ticket, error)` parses a document and never panics.
Any of the following is rejected as a `*cascade.Error` of kind
`cascade.KindInvalidInput`, with a zero `Ticket` returned alongside it:

- a document that is not valid YAML,
- a root that is not a mapping,
- a top-level key outside the 17 normative fields and the 5 extra flags,
- a missing normative field,
- an unknown key under `files_scope`,
- a value of the wrong shape for its field, or
- a `weight`, `model_class`, `cr_level`, or `qa_level` value outside its
  locked set.

`EncodeTicket(Ticket) ([]byte, error)` serializes a `Ticket` back to YAML
with its fields in normative order, followed by whichever extra flags were
set.

## Example

```yaml
id: P1-E00-W0-S00-T1
title: Example ticket
short_desc: One sentence describing the change.
full_desc: |
  What/why/how at implementation grain.
branch: P1-E00-W0-S00-T1-Example-Ticket
weight: M
model_class: build
depends_on: []
tasks:
- First waypoint
- Second waypoint
checks:
- go build ./...
acceptance_criteria:
- Every listed check is green
files_scope:
  add:
  - internal/example/example.go
  change: []
  delete: []
spec_refs:
- 06-FORGE-SPEC.md §1
cr_level: CR-B
qa_level: QA-A
sport_updates:
- 'placeholder: example/example (ADD)'
docs_updates:
- .github/wiki/Example.md
journals: true
```
