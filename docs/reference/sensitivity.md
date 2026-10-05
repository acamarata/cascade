# Sensitivity tiers and provenance

Cascade has one type for data sensitivity and one for content provenance.
Both live in [`pkg/provider`](../../pkg/provider). Every other package that
needs either one uses a Go type alias, so a value moves between packages
with no conversion and no second name table.

## Tiers

`pkg/provider.SensitivityTier` has four members. The numbers are frozen:
stored rows and old JSON carry them.

| Name | Value | Meaning |
|---|---|---|
| `restricted` | 0 | The zero value and the default. Only lanes and classes cleared for restricted content may carry it. |
| `local-only` | 1 | Never leaves the controller machine. Always an explicit choice, never a default. |
| `internal` | 2 | Normal routing across configured lanes, no public exposure. |
| `public` | 3 | No confidentiality constraint. |

### Rank

Restrictiveness does not follow the numbers. From most to least
restrictive: local-only, restricted, internal, public.
`pkg/provider.SensitivityTier.MoreRestrictiveThan` compares by rank, and a
value outside the four members ranks as the most restrictive of all.

`pkg/provider.JoinSensitivity` returns the most restrictive tier of its
arguments. With no arguments, or with any value outside the four members, it
returns local-only.

## Parsing

`pkg/provider.ParseSensitivityTier` is the only parser. It is closed:

- The four exact names parse to their tier.
- The empty string parses to restricted with no error.
- Anything else returns local-only together with an invalid-input error
  that names the value. That covers a different case (`Restricted`),
  surrounding spaces (`restricted `), an underscore (`local_only`), and
  words from other vocabularies (`secret`, `normal`).

A caller that refuses on the error refuses. A caller that drops the error
still holds the narrowest tier.

Each place that used to parse tiers on its own now goes through this
parser:

| Site | Unknown value | Empty value |
|---|---|---|
| `cascade run --sensitivity` ([`cmd/cascade/run.go`](../../cmd/cascade/run.go)) | refused | restricted |
| `conductor.execute` wire field ([`internal/daemon/conductor_execute_params.go`](../../internal/daemon/conductor_execute_params.go)) | refused | restricted |
| A stored thread privacy mode ([`internal/conversation/privacy.go`](../../internal/conversation/privacy.go)) | local-only | restricted |
| A new thread's requested privacy mode | refused | no mode set |

## Wire and stored forms

- JSON writes a tier as its name, for example `"sensitivity":"local-only"`.
- JSON reads accept the name, or a number from 0 to 3 written before the
  text form existed. JSON is how stored and locally written records come
  back (model requests, fan-out leg results, sync records), so an unknown
  name, any other number, or any other JSON type is a corrupt record: it
  decodes to local-only with an integrity error that names the value.
- A received sync batch is decoded as a whole. One record with an unknown
  tier name refuses the entire batch, its valid records included.
- A JSON `null` leaves the value as it was, as `encoding/json` does for
  every other type. Decoding into a fresh value gives the restricted zero
  value. Resetting on `null` was rejected: reset to the zero value would
  widen a reused value that held local-only, and reset to local-only would
  make `null` differ from an absent field.
- Thread privacy rows store the name. Re-writing a row stores the same bytes.
- The node dispatch wire keeps its own three words. `local-only` and
  `restricted` map to themselves, `normal` maps to internal, and anything
  else, including an empty value, maps to local-only. Internal and public
  both encode as `normal`. The bytes on that wire did not change.

### Upgrading with fan-outs in flight

The fan-out cursor and each leg record carry a digest of the JSON-encoded
model request (`resume.RequestDigest` and the conductor's leg digest). The
request's sensitivity used to encode as a number and now encodes as its
name, so a digest written by a binary from before this change does not
match one computed after it. After an upgrade, re-attaching to a fan-out
started by the old binary is refused, and replaying one of its stored leg
results fails with `ErrLegResultMismatch`. This fails closed: nothing is
routed under the wrong tier. There is no compatibility shim, because no
release before v2.0.0 promises fan-outs that survive an upgrade. Let
in-flight fan-outs finish, or start them again, after upgrading.

## Out-of-range values

A value above `public` is not a tier. The guards that hold a tier treat it
as the most restrictive case or refuse it, each on its own check:

- The egress matrix refuses it, even on a class that admits every tier.
- Egress registration refuses an out-of-range entry in a class's allowed
  tier list. The allowed list only narrows a class: admitting restricted or
  local-only content still needs the class's own flag.
- Sync eligibility, the pre-send filter and the metadata merge refuse it.
- Node placement excludes it from every node, exactly like local-only work.

## Provenance

`pkg/provider.Provenance` has two members, `trusted` and
`untrusted-source`. Text marked untrusted-source is data and never an
instruction.

- `pkg/provider.ParseProvenance` is closed. The empty string is
  untrusted-source with no error. Any other value outside the two members is
  untrusted-source with an invalid-input error.
- `pkg/provider.JoinProvenance` returns trusted only when every argument is
  exactly trusted. An untrusted, empty or unknown argument, or no argument
  at all, gives untrusted-source.

Corpus trust tags use this type. A record's effective trust is the join of
its own tag and its corpus's tag, so a record is never more trusted than
the source it came from. The stored `trust` field is the same string as
before.

## One declaration only

An arch gate in
[`internal/build/arch_sensitivity_test.go`](../../internal/build/arch_sensitivity_test.go)
refuses any new string- or integer-kind type outside `pkg/provider` whose
name looks like a sensitivity, data-class or provenance type, whose
constants use two or more tier words, or which is defined over one of the
two canonical types. A small exemption table names the declarations that
are a different concept or belong to another ticket, each with its reason.

- A type alias passes only when its chain of aliases reaches one of the two
  canonical types. Any other alias, for example one to `string` or to some
  other defined type, is checked like a declaration.
- Constants count whether they are typed (`A Tier = "restricted"`) or in
  conversion form (`A = Tier("restricted")`). A constant typed by an alias
  counts against the last declared type the alias chain reaches, so an
  alias cannot hide tier words from the type it names.
- Parentheses around the underlying type do not take a declaration out of
  scope.
- Known gap: an integer enumeration built with `iota` carries no tier words,
  so the constant rule cannot see it. Its name still has to pass the name
  rule. This is tracked as DEBT-PROC-43.
- Literals of `nodes.Requirement`, `nodes.ShipRequest` and
  `nodes.RequeueRequest` outside tests must set the tier explicitly, so no
  production placement relies on the restricted zero value.
