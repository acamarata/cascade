// Package egress implements the outbound firewall every byte leaving the
// process must pass through: a substitution pass that replaces credential
// material with typed vault-reference tags, and a sensitivity pass that
// refuses content whose declared tier the destination class does not
// admit.
//
// Purpose: interpose on outbound data paths so that no credential and no
//
//	over-classified content reaches a destination outside this process.
//
// Inputs: a registered EgressClass, an unforgeable Capability obtained
//
//	from the registry, an explicit SensitivityTier supplied by the caller,
//	and the content bytes.
//
// Outputs: the substituted bytes, safe to write, or an error. On any
//
//	error the caller has NOTHING to write: this package never returns
//	partially substituted content alongside an error.
//
// Constraints:
//
//   - FAIL CLOSED, always. Egress is the last boundary, so the safe
//     failure here is refusing to send. An unknown class, a disabled
//     class, a zero Capability, a tier the class does not admit, content
//     that is not valid UTF-8, a vault read that errors and a rewrite
//     that errors all return an error with a nil result. Refusing to send
//     costs a retry; sending unsubstituted bytes cannot be undone.
//
//   - NOT REVERSIBLE BY THE RECIPIENT. A placeholder is
//     <type>VAULT_NAME</type>: it carries the vault reference and the
//     credential kind, and nothing derived from the secret. It does not
//     encode the value, its length, its hash or its charset. Two secrets
//     of different lengths stored under the same name would substitute
//     identically, which is the property that makes a length oracle
//     impossible.
//
//   - IDEMPOTENT AND DETERMINISTIC. Substituting already-substituted
//     content changes nothing further, and identical input yields
//     identical bytes. The exact-value pass cannot match a value that is
//     already behind a tag, and the rewriter leaves an existing
//     well-formed tag untouched.
//
//   - THIS PACKAGE OWNS NO DETECTION AND NO TAG GRAMMAR. Detection is
//     internal/secrets' Detector; tag replacement is its Rewriter; the
//     grammar is its Tag. A second detector with its own calibration
//     would silently diverge from the one the rest of the system tunes,
//     so there is not one here.
//
// # Registration contract (NORMATIVE)
//
// Registration is not advisory. Every outbound byte requires a
// REGISTERED class and a Capability this package issued; a caller cannot
// construct a Capability because its only field is unexported and its
// only constructor is the registry's. Classes are refused by default: an
// unregistered class returns ErrUnknownClass before any content is
// examined, and a registered-but-disabled class returns ErrClassDisabled.
//
// The tickets that own an outbound path each register their own class and
// call Intercept before any outbound write. The owner list is:
//
//	K/S-22.T3 (conductor) · O/S-31.T3 (process plugin) · Q/S-37.T2 (node dispatch) · Q/S-38.T1 (sync engine) · W/S-48.T1 (bridge registration) · X/S-50.T1 (registry fetch) · Y/S-51.T2 (ci-poll) · Y/S-51.T6 (wiki-git-push) · AD/S-61.T2-T5 (agent-driver) · AJ/S-72.T4 (external-executor) · W/S-49.T3 (bridge, messaging instance) · Y/S-52.T2 (backend-integration) · AF/S-66.T2 (ci-mirror-push) · J/S-20.T1 (provider-intake) · F/S-12.T5 (spike-measurement) · S/S-41.T3 (backup-target) · O/S-33.T4 (plugin-remote) · X/S-50.T1 (registry-fetch, the fetch living in internal/plugins/registryfetch) · AN/S-77.T5 (fleet-discovery)
//
// TestRegistrantListByteEqual asserts that line stays byte-equal to the
// forge spec's rule-17 owner list and fails when either side moves.
//
// SPORT: EGRESS_REGISTRY: ADD · EGRESS_INTERCEPTOR: ADD ·
//
//	EGRESS_CAPABILITY: ADD (P1-E08-W2-S16-T1).
package egress
