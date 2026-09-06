// Package pews defines the PEWS ticket-schema v2 contract: the typed model
// and YAML codec for a forged ticket, as owned by the first-party cascade-pbd
// plugin (06-FORGE-SPEC.md §1).
//
// # The 17 fields
//
// A ticket carries exactly 17 fields, in this normative order: id, title,
// short_desc, full_desc, branch, weight, model_class, depends_on, tasks,
// checks, acceptance_criteria, files_scope, spec_refs, cr_level, qa_level,
// sport_updates, docs_updates. Ticket's field declaration order matches this
// list exactly, so an Encode of a Ticket always emits the fields in
// normative order.
//
// Beyond the 17, exactly five extra flags are declared and no others:
// subtickets, journals, owner_prereq, gate_only, and external_contract. Each
// is optional; DecodeTicket preserves the distinction between "omitted" and
// "present with its zero value" (an omitted bool flag decodes to a nil
// pointer, not false; an omitted string flag decodes to a nil pointer, not
// the empty string), and EncodeTicket omits exactly what was never set.
//
// # Locked value sets
//
// Weight, ModelClass, and QALevel are closed enumerations; CRLevel is a
// '+'-joined, order-preserving, duplicate-free combination of CR-A/CR-B/
// CR-C (e.g. "CR-B", "CR-A+CR-B", "CR-A+CR-B+CR-C"). DecodeTicket rejects
// any value outside these sets.
//
// # Scope
//
// This package defines ONLY the contract's shape and its codec. It does not
// persist a ticket tree, author tickets, dispatch or project work, enforce
// WHERE an extra flag may apply, or implement any later PBD lifecycle
// surface — those remain with the tickets that follow this one.
//
// # Failure mode
//
// DecodeTicket never panics. Malformed input — an unparseable document, an
// unknown or missing field, a value of the wrong shape, or a value outside a
// locked set — always returns a *cascade.Error of kind
// cascade.KindInvalidInput, never a partially populated Ticket.
package pews
