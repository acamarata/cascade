package rpc

import (
	"encoding/json"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

// codeElevationRequired reuses pkg/cascade's existing application error
// code table verbatim (RPCCodeElevationRequired = -32008, the wire code for
// cascade.KindElevationRequired) rather than defining a second mapping —
// this is the one case in this package where an application-taxonomy code,
// not a protocol-framing code, is the correct choice: ELEVATION_REQUIRED is
// exactly what the taxonomy's KindElevationRequired kind exists to name.
const codeElevationRequired = cascade.RPCCodeElevationRequired

// elevatedEnvelope is the params wire shape for a call to an elevated
// method. On a FIRST call (no attestation yet), a client sends its normal
// params directly and the whole params blob is what gets hashed and bound
// into the nonce; on a RETRY carrying a satisfying attestation, the client
// wraps its original params under "_args" alongside "_attestation". This
// concrete envelope shape is a design decision this ticket makes explicit
// (see elevation_attest.go's signedFields doc comment for the parallel
// decision on the signed-bytes format) since S-07.T6, which mints the
// attestation client-side, had not landed a wire convention as of this
// ticket.
type elevatedEnvelope struct {
	Attestation *Attestation    `json:"_attestation,omitempty"`
	Args        json.RawMessage `json:"_args,omitempty"`
}

// elevationRequiredData is the ELEVATION_REQUIRED error's Data payload:
// the nonce the client's next attestation-bearing retry must present.
type elevationRequiredData struct {
	Reason string `json:"reason"`
	Nonce  string `json:"nonce"`
}

// The three helpers below back the conditional rules in elevationTable.
// All of them FAIL CLOSED: when a helper cannot prove that a request is the
// harmless case, it reports true and the verb is elevated.
//
// The direction matters more than it looks. These helpers do not decide
// whether a request is valid, they decide whether it needs authorisation.
// A helper that returns false on input it could not parse hands an attacker
// a bypass: send the privileged verb with a payload the helper chokes on and
// the elevation gate disappears, while the handler downstream may still
// decode the payload its own way and act on it. Refusing to guess, and
// demanding elevation whenever the answer is unclear, costs a legitimate
// caller one authorisation prompt on a malformed request. Guessing the other
// way costs the user the gate.

// unparseableParams reports whether raw cannot be read as a JSON object.
// Callers treat this as "elevate": params we cannot read cannot be shown to
// be a narrowing.
//
// Keys are folded to lower case, because a handler decoding into a struct
// matches field names case-insensitively: {"Direction":"loosen"} reaches a
// handler as direction=loosen while an exact-case lookup sees no direction
// key at all. Two keys that fold together are ambiguous (the decoder keeps
// the last one, a reader of this map would keep another), so they are
// unreadable too and elevate.
func unparseableParams(raw json.RawMessage) (map[string]json.RawMessage, bool) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, true
	}
	folded := make(map[string]json.RawMessage, len(m))
	for k, v := range m {
		lower := strings.ToLower(k)
		if _, dup := folded[lower]; dup {
			return nil, true
		}
		folded[lower] = v
	}
	return folded, false
}

// hasJSONField reports whether raw contains key with any non-null value.
// Unreadable params elevate.
func hasJSONField(raw json.RawMessage, key string) bool {
	m, bad := unparseableParams(raw)
	if bad {
		return true
	}
	v, ok := m[key]
	if !ok {
		return false
	}
	return string(v) != "null"
}

// jsonFieldEquals reports whether raw's key field equals want. Unreadable
// params elevate, and so does a value present in some shape other than the
// string this rule expects, because such a value cannot be shown to differ
// from want.
func jsonFieldEquals(raw json.RawMessage, key, want string) bool {
	m, bad := unparseableParams(raw)
	if bad {
		return true
	}
	v, ok := m[key]
	if !ok {
		return false
	}
	var s string
	if err := json.Unmarshal(v, &s); err != nil {
		return true
	}
	return s == want
}

// jsonFieldEnables reports whether raw's key field requests turning a flag
// ON. It accepts the JSON boolean true and the string "true", because both
// reach the daemon in practice: a typed client marshals a Go bool, while a
// shell client sending hand-built JSON often quotes it.
//
// Only a value that positively reads as OFF returns false. An absent key
// returns false as well, since a request that does not mention the flag is
// not turning it on. Everything else elevates.
func jsonFieldEnables(raw json.RawMessage, key string) bool {
	m, bad := unparseableParams(raw)
	if bad {
		return true
	}
	v, ok := m[key]
	if !ok {
		return false
	}
	var b bool
	if err := json.Unmarshal(v, &b); err == nil {
		return b
	}
	var s string
	if err := json.Unmarshal(v, &s); err == nil {
		switch strings.ToLower(strings.TrimSpace(s)) {
		case "false", "0", "no", "off", "":
			return false
		}
		return true
	}
	return true
}

// specElevationRows are the static rows of the elevated-verb table,
// transcribed from 06-FORGE-SPEC.md's numbered item 14 (cited by this
// ticket's contract and by 15-T0-RULINGS-R14.md as "§5.14"), amended by
// R-14.48 for enable_remote_runtime and by the P1-SEC rows below.
// elevation_test.go's TestElevationTableMatchesSpec independently
// re-transcribes the same spec prose into a second data structure and asserts
// set-equality against the table's method names, so a typo or omission here
// fails that test rather than merely reflecting itself.
//
// They are registered through RegisterElevatedVerb by a package-level
// variable initialiser (see below), which Go completes before any init
// function of this package or of an importer runs: a verb registered from an
// init therefore always lands after these rows and before the table seals.
var specElevationRows = []struct {
	method string
	rule   ElevationRule
}{
	{"vault.get", ElevationRule{Always: true}},
	{"vault.rotate", ElevationRule{Always: true}},
	{"approval.grant", ElevationRule{Always: true}},
	{"standing_grant.create", ElevationRule{Always: true}},
	{"standing_grant.change", ElevationRule{Always: true}},
	{"backup.create", ElevationRule{Always: true}},
	{"backup.export", ElevationRule{Always: true}},
	{"backup.import", ElevationRule{Always: true}},
	{"backup.restore", ElevationRule{Always: true}},
	{"backup.key_export", ElevationRule{Always: true}},
	{"backup.key_import", ElevationRule{Always: true}},
	// plugin.add and plugin.update are always elevated. §5.14 elevated plugin
	// add only when the request carried process_tier or grant_expand, but the
	// CLI never sends either key, so the conditional left the verb ungated
	// for every real caller. A plugin install changes what runs on the
	// machine; the prompt is the point.
	{"plugin.add", ElevationRule{Always: true}},
	{"plugin.update", ElevationRule{Always: true}},
	{"perms.grant", ElevationRule{Always: true}},
	{"perms.revoke", ElevationRule{Always: true}},
	{"node.enroll", ElevationRule{Always: true}},
	{"node.remove", ElevationRule{Always: true}},
	{"node.upgrade", ElevationRule{Always: true}},
	// sync.conflicts_resolve is elevated when the resolution DISCARDS the
	// server-primary side, per §5.14 ("when discarding server-primary").
	//
	// This rule used to read the other way round — it elevated
	// `{"resolution":"server-primary"}`, which is the side that keeps
	// what the merge already decided and changes nothing — so the gate
	// stood in front of the harmless verb and the destructive one went
	// through ungated. It also read a params field (`resolution`) that no
	// caller sends: internal/sync's surface takes `keep`, so even the
	// inverted rule never matched a real request (R-14.273).
	{"sync.conflicts_resolve", ElevationRule{Conditional: discardsServerPrimary}},
	// policy/sensitivity changes are elevated only when they LOOSEN
	// (widen) the resolved value, per §5.14 + §5 item 16's "widening a
	// resolved tier is a LOOSENING (elevated verb); narrowing is always
	// allowed."
	{"policy.set", ElevationRule{Conditional: func(p json.RawMessage) bool {
		return jsonFieldEquals(p, "direction", "loosen")
	}}},
	{"sensitivity.set", ElevationRule{Conditional: func(p json.RawMessage) bool {
		return jsonFieldEquals(p, "direction", "loosen")
	}}},
	{"elevation.set_allow_remote", ElevationRule{Conditional: func(p json.RawMessage) bool {
		return jsonFieldEnables(p, "allow_remote") || jsonFieldEnables(p, "enable")
	}}},
	// R-14.48: enabling [plugins].enable_remote_runtime is elevated.
	{"plugins.set_enable_remote_runtime", ElevationRule{Conditional: func(p json.RawMessage) bool {
		return jsonFieldEnables(p, "enable_remote_runtime") || jsonFieldEnables(p, "enable")
	}}},
	{"uninstall.purge_data", ElevationRule{Always: true}},
	// config.accept_loosening (P1-SEC-04) and policy.authorize (P1-SEC-10)
	// are registered here, so this file is the one writer of the static
	// table. Both stay always-elevated: a handler that decides server-side
	// whether a request loosens may narrow its row; a client-declared flag
	// never does.
	{"config.accept_loosening", ElevationRule{Always: true}},
	{"policy.authorize", ElevationRule{Always: true}},
}

// registerSpecElevationRows fills the table from specElevationRows. It is the
// initialiser of a package-level variable so it runs before every init.
func registerSpecElevationRows() struct{} {
	for _, r := range specElevationRows {
		RegisterElevatedVerb(r.method, r.rule)
	}
	return struct{}{}
}

var _ = registerSpecElevationRows()

// IsElevated reports whether method (with the given raw params) is an
// elevated verb per the elevation table. Reading the table seals it.
func IsElevated(method string, params json.RawMessage) bool {
	rule, ok := lookupElevationRule(method)
	if !ok {
		return false
	}
	if rule.always {
		return true
	}
	return rule.conditional != nil && rule.conditional(params)
}
