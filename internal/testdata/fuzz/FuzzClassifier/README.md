# internal/testdata/fuzz/FuzzClassifier seed corpus

Seed corpus for `internal/policy.FuzzClassifier`
(`internal/policy/classifier_test.go`), per 06-FORGE-SPEC.md §5 rule 7
("any ticket adding a parser/decoder MUST include a `FuzzXxx` target in
checks; corpora live at `internal/testdata/fuzz/...`, never repo root")
and this module's established convention of putting every `FuzzXxx`
corpus under this shared home rather than beside its owning package (see
the sibling `FuzzChunk`, `FuzzMCPFrame`, `FuzzParseRequest` directories).

`classifier_test.go` reads every file in this directory except this
README at `FuzzClassifier`'s setup time and seeds each one's raw bytes
via `f.Add`, alongside a few literal inputs a file cannot express as
cleanly (the empty string, a lone NUL byte).

## Provenance (Article 2)

A fuzz seed corpus has no real counterpart to harvest from. Its purpose
is to start the mutator from shapes that reach every branch of the
classifier. Every seed here is hand-authored to a stated purpose, one per
rung of the §5.15 ladder plus the shapes that exercise the
fail-closed paths:

- `seed_l0_read_only.txt`: a read-only command, the L0 rung.
- `seed_l1_local_dev.txt`: a test invocation, the L1 rung, and the
  subcommand-refinement path.
- `seed_l2_workspace_mutation.txt`: a staging command, the L2 rung.
- `seed_l3_external_side_effect.txt`: a push, the L3 rung.
- `seed_l4_destructive.txt`: a recursive delete, the L4 rung.
- `seed_wrapper_unresolvable.txt`: a wrapper whose inner command is a
  parameter expansion, the "unresolvable inner ⇒ L4" rule.
- `seed_obfuscated_chain.txt`: variable indirection, command
  substitution inside a double-quoted word, a backquoted substitution
  nested in it, an operator chain and an output redirection in one line,
  so the mutator starts from an input that already touches every
  indirection path at once.
- `seed_unmodeled_forms.txt`: nested conditional and loop clauses, the
  shapes the node-form allow list refuses.
