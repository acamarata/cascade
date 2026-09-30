package runtime

// Purpose: regression tests for the S-103.T1 review's proofs of concept
//   against the config-write guards: a foreign plugin table, a forged
//   managed record, trailing-text literal injection (through ApplyDiff AND
//   Set), and secret-shaped or userinfo-bearing literals.
// Inputs: n/a (test-only, every write under t.TempDir()).
// Outputs: n/a (test-only).
// Constraints: every case asserts the file is byte-identical afterwards,
//   so a refusal that still wrote something fails.
// SPORT: internal/runtime config_diff.go (TEST) — P1-E25-W5-S103-T1.

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// guardSeed is a config every case starts from, so "byte-identical" is a
// comparison against real content rather than against an absent file.
const guardSeed = "[runtime]\nprofile = \"local\"\n"

// assertUnchanged fails t unless w.Path still holds exactly guardSeed.
func assertUnchanged(t *testing.T, w *ConfigWriter) {
	t.Helper()
	data, err := os.ReadFile(w.Path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if string(data) != guardSeed {
		t.Fatalf("config changed by a refused write:\n%s", data)
	}
}

func assertKind(t *testing.T, err error, want cascade.Kind) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected a %v refusal, got nil", want)
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != want {
		t.Fatalf("KindOf(%v) = (%v, %v), want %v", err, kind, ok, want)
	}
}

func TestApplyDiffRefusesForeignPluginTable(t *testing.T) {
	for _, path := range []string{
		"plugins.other.x",
		"plugins.enable_remote_runtime",
		"plugins.cascade-nself.managed",
		"plugins.cascade-nself.managed.x",
		"plugins.cascade-nself.managed.runtime__profile",
	} {
		t.Run(path, func(t *testing.T) {
			w := writerAt(t, guardSeed)
			_, err := w.ApplyDiff(ConfigDiff{Owner: "cascade-nself", Entries: []DiffEntry{{Path: path, Literal: "true"}}})
			assertKind(t, err, cascade.KindPolicyDenied)
			assertUnchanged(t, w)
		})
	}
}

// TestApplyDiffRefusesManagedForging is the review's forging proof: a diff
// that writes its own managed record for a user-set key must be refused,
// and a later honest diff must still see the user's value as user-set.
func TestApplyDiffRefusesManagedForging(t *testing.T) {
	w := writerAt(t, guardSeed)
	forge := ConfigDiff{Owner: "cascade-nself", Entries: []DiffEntry{
		{Path: "plugins.cascade-nself.managed.runtime__profile", Literal: `"\"local\""`},
		{Path: "runtime.profile", Literal: `"server"`},
	}}
	_, err := w.ApplyDiff(forge)
	assertKind(t, err, cascade.KindPolicyDenied)
	assertUnchanged(t, w)

	res, err := w.ApplyDiff(ConfigDiff{Owner: "cascade-nself", Entries: []DiffEntry{{Path: "runtime.profile", Literal: `"server"`}}})
	if err != nil {
		t.Fatalf("honest ApplyDiff: %v", err)
	}
	if len(res.Skipped) != 1 || res.Skipped[0].Reason != "user-set" {
		t.Fatalf("result = %+v, want runtime.profile skipped as user-set", res)
	}
	assertUnchanged(t, w)
}

// injectionPayloads are the review's two trailing-text literals: each
// parses as a string followed by a guarded table the caller never named.
var injectionPayloads = map[string]string{
	"agents.egress":                "\"a\"\n[agents.egress]\nallowlist = [\"*\"]",
	"conductor.external_routing":   "\"a\"\n[conductor]\nexternal_routing_enabled = true",
	"second key, same line family": "\"a\"\nextra = 1",
}

func assertLiteralRefusal(t *testing.T, err error) {
	t.Helper()
	var lit *LiteralError
	if !errors.As(err, &lit) {
		t.Fatalf("err = %v, want a *LiteralError from the shared literal validator", err)
	}
}

func TestApplyDiffRefusesTableInjection(t *testing.T) {
	for name, payload := range injectionPayloads {
		t.Run(name, func(t *testing.T) {
			w := writerAt(t, guardSeed)
			_, err := w.ApplyDiff(ConfigDiff{Owner: "cascade-nself", Entries: []DiffEntry{
				{Path: "plugins.cascade-nself.x", Literal: payload},
			}})
			assertLiteralRefusal(t, err)
			assertUnchanged(t, w)
		})
	}
}

func TestConfigSetRefusesTableInjection(t *testing.T) {
	for name, payload := range injectionPayloads {
		t.Run(name, func(t *testing.T) {
			w := writerAt(t, guardSeed)
			_, err := w.Set("runtime.profile", payload)
			assertLiteralRefusal(t, err)
			assertUnchanged(t, w)
		})
	}
}

func TestApplyDiffRefusesSecretLiteral(t *testing.T) {
	cases := map[string]string{
		"bearer prefix":   `"sk-` + `live-` + strings.Repeat("a", 24) + `"`,
		"userinfo dsn":    `"postgres://admin:hunter2@db:5432/app"`,
		"inside an array": `["ok", "https://u:p@example.test/x"]`,
	}
	for name, lit := range cases {
		t.Run(name, func(t *testing.T) {
			w := writerAt(t, guardSeed)
			_, err := w.ApplyDiff(ConfigDiff{Owner: "cascade-nself", Entries: []DiffEntry{
				{Path: "plugins.cascade-nself.postgres_host", Literal: lit},
			}})
			assertKind(t, err, cascade.KindPolicyDenied)
			var secret *SecretLiteralError
			if !errors.As(err, &secret) {
				t.Fatalf("err = %v, want *SecretLiteralError", err)
			}
			assertUnchanged(t, w)
			_, err = w.Set("plugins.cascade-nself.postgres_host", lit)
			if !errors.As(err, &secret) {
				t.Fatalf("Set accepted the same forbidden value: %v", err)
			}
			assertUnchanged(t, w)
		})
	}
}

// TestApplyDiffRefusesDuplicatePath: the same path twice in one diff is
// ambiguous (the review saw two "applied" outcomes, last one winning).
func TestApplyDiffRefusesDuplicatePath(t *testing.T) {
	w := writerAt(t, guardSeed)
	_, err := w.ApplyDiff(ConfigDiff{Owner: "cascade-nself", Entries: []DiffEntry{
		{Path: "plugins.cascade-nself.x", Literal: "1"},
		{Path: "plugins.cascade-nself.x", Literal: "2"},
	}})
	assertKind(t, err, cascade.KindPolicyDenied)
	if !strings.Contains(err.Error(), "plugins.cascade-nself.x") {
		t.Fatalf("err = %v, want it to name the duplicated path", err)
	}
	assertUnchanged(t, w)
}

// TestApplyDiffRefusesUnnamedChange drives gateCandidate directly: a
// composed file that changes a key the diff never named (here a hooks
// table appearing) is refused. Vetting plus canonical encoding make this
// unreachable through ApplyDiff today; the gate stays as the last check
// that the line editor put every value exactly where it was asked to.
func TestApplyDiffRefusesUnnamedChange(t *testing.T) {
	current, err := decodeForValidate([]byte(guardSeed))
	if err != nil {
		t.Fatalf("decode seed: %v", err)
	}
	applied := []DiffOutcome{{Path: "plugins.cascade-nself.x", Reason: "absent"}}
	named := []byte(guardSeed + "\n[plugins.cascade-nself]\nx = 1\nmanaged = {\"plugins.cascade-nself.x\" = \"1\"}\n")
	if err := gateCandidate(current, named, "cascade-nself", applied); err != nil {
		t.Fatalf("a candidate changing only the named path was refused: %v", err)
	}
	unnamed := append(append([]byte{}, named...), []byte("\n[hooks.h]\ncommand = \"x\"\n")...)
	err = gateCandidate(current, unnamed, "cascade-nself", applied)
	assertKind(t, err, cascade.KindPolicyDenied)
	if !strings.Contains(err.Error(), "hooks.h.command") {
		t.Fatalf("err = %v, want it to name hooks.h.command", err)
	}
}

// TestApplyDiffRefusesOversizedPluginTable includes the managed record in the bound.
func TestApplyDiffRefusesOversizedPluginTable(t *testing.T) {
	w := writerAt(t, guardSeed)
	_, err := w.ApplyDiff(ConfigDiff{Owner: "cascade-nself", Entries: []DiffEntry{
		{Path: "plugins.cascade-nself.large", Literal: `"` + strings.Repeat("a ", 33<<10) + `"`},
	}})
	assertKind(t, err, cascade.KindPolicyDenied)
	if !strings.Contains(err.Error(), "65536-byte bound") {
		t.Fatalf("wrong refusal: %v", err)
	}
	assertUnchanged(t, w)
}
