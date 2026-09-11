// Purpose: the cascade.hello / cascade.hello_ack version-negotiation
//
//	exchange every launched plugin process performs before its transport
//	is considered ready for ordinary calls.
//
// Inputs: a *Transport already reading the plugin's stdout, and the
//
//	manifest's minimum accepted protocol version.
//
// Outputs: the plugin's HelloAckMsg on success; on a version below the
//
//	host minimum, a cascade.version_mismatch notification is sent and a
//	typed ErrVersionMismatch is returned.
//
// Constraints: fail closed — any decode failure or a version the host
//
//	does not accept refuses the handshake rather than proceeding.
//
// SPORT: internal/plugins/process handshake (ADD) — P1-E15-W4-S31-T3.

package process

import (
	"context"
	"encoding/json"

	"github.com/acamarata/cascade/pkg/cascade"
)

// performHandshake sends cascade.hello with minVersion, awaits
// cascade.hello_ack, and refuses a plugin version below minVersion. On
// refusal it sends cascade.version_mismatch before returning the error;
// the caller is responsible for terminating the process.
func performHandshake(ctx context.Context, t *Transport, name, minVersion string) (HelloAckMsg, error) {
	hello := HelloMsg{MinProtocolVersion: minVersion}
	params, err := json.Marshal(hello)
	if err != nil {
		return HelloAckMsg{}, cascade.Wrap(cascade.KindInvalidInput, err, "process: encoding cascade.hello")
	}
	result, err := t.Call(ctx, "cascade.hello", params)
	if err != nil {
		return HelloAckMsg{}, cascade.Wrap(cascade.KindUnavailable, err, "process: cascade.hello handshake failed")
	}
	var ack HelloAckMsg
	if err := json.Unmarshal(result, &ack); err != nil {
		return HelloAckMsg{}, cascade.Wrap(cascade.KindIntegrity, err, "process: decoding cascade.hello_ack")
	}
	if versionLess(ack.ProtocolVersion, minVersion) {
		sendVersionMismatch(t, minVersion, ack.ProtocolVersion)
		return HelloAckMsg{}, wrapVersionMismatch(name, ack.ProtocolVersion, minVersion)
	}
	return ack, nil
}

// sendVersionMismatch notifies the plugin of the refusal. The send is
// best-effort: the process is about to be terminated regardless of
// whether the notification lands.
func sendVersionMismatch(t *Transport, minVersion, pluginVersion string) {
	params, err := json.Marshal(VersionMismatchMsg{MinProtocolVersion: minVersion, PluginVersion: pluginVersion})
	if err != nil {
		return
	}
	_ = t.Notify("cascade.version_mismatch", params)
}

// versionLess reports whether a is a lower semver than b, comparing
// major.minor.patch numerically. A malformed component compares as
// lower than any well-formed one, so a plugin that cannot even report a
// parseable version fails closed rather than being admitted.
func versionLess(a, b string) bool {
	av, aok := parseSemver(a)
	bv, bok := parseSemver(b)
	if !aok {
		return true
	}
	if !bok {
		return false
	}
	for i := 0; i < 3; i++ {
		if av[i] != bv[i] {
			return av[i] < bv[i]
		}
	}
	return false
}

// parseSemver parses a strict "major.minor.patch" string into three
// non-negative integers. ok is false for anything else, including a
// pre-release suffix or a missing component.
func parseSemver(s string) (v [3]int, ok bool) {
	part, rest := 0, s
	for part < 3 {
		var digits string
		if idx := indexByte(rest, '.'); idx >= 0 {
			digits, rest = rest[:idx], rest[idx+1:]
		} else {
			digits, rest = rest, ""
		}
		n, valid := parseDigits(digits)
		if !valid {
			return v, false
		}
		v[part] = n
		part++
		if part < 3 && rest == "" {
			return v, false
		}
	}
	return v, rest == ""
}

// indexByte finds the first occurrence of c in s, or -1.
func indexByte(s string, c byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			return i
		}
	}
	return -1
}

// parseDigits converts a non-empty run of ASCII digits to an int.
func parseDigits(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	n := 0
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
		n = n*10 + int(s[i]-'0')
	}
	return n, true
}
