// Package envelope is the one canonical scope envelope (ADR-0007): a
// controller-signed statement of the roots, tools, commands, egress
// classes, credential grants, budget, fencing token, sensitivity and
// principal one unit of delegated work may use.
//
// The controller signs Canonical(e) with its installation key (Issue); a
// receiver checks the signature against the pinned controller key
// (Verify), and a delegating holder derives a smaller envelope with Narrow,
// which refuses any child that grants more than its parent in any field.
// Decode is the bounded decoder for untrusted wire bytes.
package envelope
