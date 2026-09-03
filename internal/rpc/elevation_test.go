package rpc

import (
	"encoding/json"
	"strings"
	"testing"
)

// specVerbListVerbatim is copied verbatim from 06-FORGE-SPEC.md's numbered
// item 14 (the ticket's "§5.14"), amended by R-14.48's
// enable_remote_runtime clause. This is a SECOND, independent
// transcription from elevationTable's own — a different data structure
// (prose, split and mapped by translateSpecVerb below) parsed by different
// code — so a typo or omission in ONE of the two transcriptions is caught
// by TestElevationTableMatchesSpec comparing them, rather than the table
// merely proving it matches itself.
const specVerbListVerbatim = `vault get/rotate · approval grant · standing-grant ` +
	`create/change · backup create/export/import/restore/key export/key import · ` +
	`plugin add (process-tier or grant expansion)/perms grant/perms revoke · ` +
	`node enroll/remove/upgrade · sync conflicts resolve (discarding server-primary) · ` +
	`policy or sensitivity loosening · enabling remote elevation (allow_remote) · ` +
	`enabling the remote plugin runtime (enable_remote_runtime) · uninstall --purge-data`

// translateSpecVerb maps one englishy clause from specVerbListVerbatim to
// the one or more RPC method names elevationTable registers for it. This
// mapping is the ONLY place spec prose and Go method-name spelling are
// bridged; elevationTable itself never appears on the right-hand side of
// this map by construction (it's the reference the test compares AGAINST).
var specVerbToMethods = map[string][]string{
	"vault get/rotate":             {"vault.get", "vault.rotate"},
	"approval grant":               {"approval.grant"},
	"standing-grant create/change": {"standing_grant.create", "standing_grant.change"},
	"backup create/export/import/restore/key export/key import": {
		"backup.create", "backup.export", "backup.import", "backup.restore",
		"backup.key_export", "backup.key_import",
	},
	"plugin add (process-tier or grant expansion)/perms grant/perms revoke": {
		"plugin.add", "perms.grant", "perms.revoke",
	},
	"node enroll/remove/upgrade":                                 {"node.enroll", "node.remove", "node.upgrade"},
	"sync conflicts resolve (discarding server-primary)":         {"sync.conflicts_resolve"},
	"policy or sensitivity loosening":                            {"policy.set", "sensitivity.set"},
	"enabling remote elevation (allow_remote)":                   {"elevation.set_allow_remote"},
	"enabling the remote plugin runtime (enable_remote_runtime)": {"plugins.set_enable_remote_runtime"},
	"uninstall --purge-data":                                     {"uninstall.purge_data"},
}

// TestElevationTableMatchesSpec independently re-derives the expected
// method-name set from specVerbListVerbatim and asserts it is IDENTICAL
// (both directions: nothing missing, nothing extra) to elevationTable's
// registered method names. This is the cross-check this ticket's brief
// requires: a table test that would fail if elevationTable dropped or
// mis-spelled an entry, because the expected set comes from a wholly
// separate parse of the spec prose, not from elevationTable itself.
func TestElevationTableMatchesSpec(t *testing.T) {
	clauses := strings.Split(specVerbListVerbatim, " · ")
	wantMethods := map[string]bool{}
	for _, clause := range clauses {
		methods, ok := specVerbToMethods[clause]
		if !ok {
			t.Fatalf("no translation registered for spec clause %q — "+
				"specVerbToMethods is out of sync with specVerbListVerbatim", clause)
		}
		for _, m := range methods {
			wantMethods[m] = true
		}
	}

	gotMethods := map[string]bool{}
	for _, rule := range elevationTable {
		gotMethods[rule.method] = true
	}

	for m := range wantMethods {
		if !gotMethods[m] {
			t.Errorf("spec requires elevated method %q but elevationTable does not register it", m)
		}
	}
	for m := range gotMethods {
		if !wantMethods[m] {
			t.Errorf("elevationTable registers %q but it is not derivable from the spec verb list", m)
		}
	}
	if len(clauses) != 11 {
		t.Errorf("spec verb list has %d clauses, expected 11 (drift in specVerbListVerbatim itself)", len(clauses))
	}
}

func TestIsElevated_AlwaysVerbs(t *testing.T) {
	always := []string{
		"vault.get", "vault.rotate", "approval.grant",
		"standing_grant.create", "standing_grant.change",
		"backup.create", "backup.export", "backup.import", "backup.restore",
		"backup.key_export", "backup.key_import",
		"perms.grant", "perms.revoke",
		"node.enroll", "node.remove", "node.upgrade",
		"uninstall.purge_data",
	}
	for _, m := range always {
		if !IsElevated(m, nil) {
			t.Errorf("IsElevated(%q, nil) = false, want true (unconditional verb)", m)
		}
	}
}

func TestIsElevated_NonElevatedVerb(t *testing.T) {
	if IsElevated("status.get", nil) {
		t.Error("status.get must not be elevated")
	}
}

func TestIsElevated_ConditionalVerbs(t *testing.T) {
	cases := []struct {
		method string
		params string
		want   bool
	}{
		{"plugin.add", `{"name":"x"}`, false},
		{"plugin.add", `{"name":"x","process_tier":"trusted"}`, true},
		{"plugin.add", `{"name":"x","grant_expand":true}`, true},
		{"sync.conflicts_resolve", `{"resolution":"client-primary"}`, false},
		{"sync.conflicts_resolve", `{"resolution":"server-primary"}`, true},
		{"policy.set", `{"direction":"narrow"}`, false},
		{"policy.set", `{"direction":"loosen"}`, true},
		{"sensitivity.set", `{"direction":"loosen"}`, true},
		{"elevation.set_allow_remote", `{"enable":true}`, true},
		{"plugins.set_enable_remote_runtime", `{"enable":true}`, true},
	}
	for _, c := range cases {
		got := IsElevated(c.method, json.RawMessage(c.params))
		if got != c.want {
			t.Errorf("IsElevated(%q, %s) = %v, want %v", c.method, c.params, got, c.want)
		}
	}
}

// TestElevation_FailsClosedOnEveryWireShape pins the fail-closed contract of
// the conditional rules. It exists because the first version of these
// helpers decoded a flag only as a Go string, so the natural wire form
// {"allow_remote": true} failed the decode, the rule returned false, and the
// privileged verb ran with no elevation gate at all. Every row below is a
// shape a real client can send.
func TestElevation_FailsClosedOnEveryWireShape(t *testing.T) {
	for _, tc := range []struct {
		name   string
		method string
		params string
		want   bool
	}{
		// The bypass that shipped: a JSON boolean, which is what any
		// typed client marshalling a Go bool actually sends.
		{"bool true elevates", "elevation.set_allow_remote", `{"allow_remote": true}`, true},
		{"string true elevates", "elevation.set_allow_remote", `{"allow_remote": "true"}`, true},
		{"runtime bool true elevates", "plugins.set_enable_remote_runtime", `{"enable_remote_runtime": true}`, true},
		{"runtime string true elevates", "plugins.set_enable_remote_runtime", `{"enable_remote_runtime": "true"}`, true},

		// Turning a flag OFF is a narrowing and needs no elevation.
		{"bool false does not elevate", "elevation.set_allow_remote", `{"allow_remote": false}`, false},
		{"string false does not elevate", "elevation.set_allow_remote", `{"allow_remote": "false"}`, false},
		{"absent flag does not elevate", "elevation.set_allow_remote", `{"something_else": 1}`, false},

		// Anything unreadable must elevate rather than sail through.
		{"malformed json elevates", "elevation.set_allow_remote", `{"allow_remote":`, true},
		{"non-object params elevate", "elevation.set_allow_remote", `["allow_remote"]`, true},
		{"number value elevates", "elevation.set_allow_remote", `{"allow_remote": 1}`, true},
		{"object value elevates", "elevation.set_allow_remote", `{"allow_remote": {"x":1}}`, true},
		// JSON null params decode to an empty object, which is the same
		// as an absent key: the request names no flag, so it turns
		// nothing on. This is only safe while the handler requires the
		// flag explicitly and never defaults it to enabled.
		{"null params do not elevate", "elevation.set_allow_remote", `null`, false},

		// The same rule for the string-valued conditionals.
		{"loosen elevates", "policy.set", `{"direction": "loosen"}`, true},
		{"narrow does not elevate", "policy.set", `{"direction": "narrow"}`, false},
		{"malformed policy params elevate", "policy.set", `{"direction":`, true},
		{"non-string direction elevates", "policy.set", `{"direction": 7}`, true},
		{"sensitivity malformed elevates", "sensitivity.set", `not json`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := IsElevated(tc.method, json.RawMessage(tc.params))
			if got != tc.want {
				t.Errorf("IsElevated(%q, %s) = %v, want %v", tc.method, tc.params, got, tc.want)
			}
		})
	}
}

// TestElevation_UnknownVerbNeverElevates guards the other direction: a verb
// absent from the table is not silently treated as privileged.
func TestElevation_UnknownVerbNeverElevates(t *testing.T) {
	if IsElevated("status.get", json.RawMessage(`{}`)) {
		t.Error("a read verb absent from the table must not be elevated")
	}
}
