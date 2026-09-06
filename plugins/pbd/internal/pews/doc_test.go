package pews

import "fmt"

// ExampleDecodeTicket demonstrates decoding a minimal ticket document and
// reading back the fields.
func ExampleDecodeTicket() {
	doc := []byte(`id: P1-E00-W0-S00-T1
title: Example ticket
short_desc: One sentence.
full_desc: |
  Longer description.
branch: P1-E00-W0-S00-T1-Example-Ticket
weight: S
model_class: mech
depends_on: []
tasks:
- Do the one thing
checks:
- go build ./...
acceptance_criteria:
- The check above is green
files_scope:
  add:
  - internal/example/example.go
  change: []
  delete: []
spec_refs:
- 06-FORGE-SPEC.md §1
cr_level: CR-A+CR-B
qa_level: QA-A
sport_updates:
- 'placeholder: example/example (ADD)'
docs_updates:
- .github/wiki/Example.md
`)

	ticket, err := DecodeTicket(doc)
	if err != nil {
		panic(err)
	}
	fmt.Println(ticket.ID, ticket.Weight, ticket.ModelClass, ticket.CRLevel)
	fmt.Println("journals set:", ticket.Journals != nil)

	// Output:
	// P1-E00-W0-S00-T1 S mech CR-A+CR-B
	// journals set: false
}
