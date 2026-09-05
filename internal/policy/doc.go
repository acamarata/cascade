// Package policy defines capabilities, permissions, the deny-list, and
// autonomy levels that gate what an agent may do.
//
// Every action reaches one decision through Engine.Evaluate, which runs a
// seven-layer stack in this normative order (R-21.236, R-14.26):
//
//	layer 0 — data-class check (UNCONDITIONAL, outside first-match-wins;
//	          a refusal is the terminal error ErrDataClassDenied)
//	layer 1 — deny-list
//	layer 2 — elevation check
//	layer 3 — standing grants
//	layer 4 — capability default policy
//	layer 5 — autonomy profile
//	layer 6 — fail-closed fallback
//
// Layer 2 consults ONLY the in-memory elevation nonce ledger, which is the
// attestation-replay ledger. The approval queue's ledger is approval-token
// replay and is NEVER consulted by layer 2.
//
// A valid same-turn authorization at layer 1 records the override and
// continues to layer 2; it never allows and never skips the elevation
// check (R-21.231).
package policy
