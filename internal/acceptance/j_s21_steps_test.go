//go:build integration

package acceptance

// Purpose (this file): the drill's individual STEPS — one function per
//   command an operator runs, each asserting what that command must
//   answer. Split from j_s21_script_test.go, which keeps the harness and
//   the order, for Art.10.3's 300-line cap.
// SPORT: acceptance/j-s21-compat-sub (ADD) — P1-E10-W3-S21-T3.

import (
	"strings"
	"testing"
)

// drillIntake is step 1: the provider is added, probed and live-verified.
func drillIntake(t *testing.T, d drillEnv, target compatSubTarget) {
	t.Helper()
	args := []string{"provider", "add", providerName, "--key-env", target.KeyEnv, "--base-url", target.BaseURL}
	if target.Kind != "" {
		args = append(args, "--kind", target.Kind)
	}
	out := d.mustRun(t, args...)
	for _, want := range []string{providerName, target.BaseURL} {
		if !strings.Contains(out, want) {
			t.Errorf("`provider add` did not report %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "none") && strings.Contains(out, "models") {
		t.Errorf("the endpoint enumerated no models, so the micro-verify had nothing to verify:\n%s", out)
	}
	// A separate health probe, because adding is not probing: the
	// registry records health from `provider test`, and doctor reads that
	// column. A drill that skipped this would find doctor reporting the
	// provider "never probed" and would have no idea why.
	d.mustRun(t, "provider", "test", providerName)
}

// drillRegistry is step 2: what S-20.T2 populated is what `list` reports.
func drillRegistry(t *testing.T, d drillEnv) {
	t.Helper()
	envelope := decodeJSON(t, jsonTail(d.mustRun(t, "provider", "list", "--json")))
	data, _ := envelope["data"].(map[string]any)
	rows, _ := data["providers"].([]any)
	if len(rows) == 0 {
		t.Fatalf("`provider list --json` reports no providers after one was added:\n%v", envelope)
	}
	var row map[string]any
	for _, r := range rows {
		m, _ := r.(map[string]any)
		if m["name"] == providerName {
			row = m
		}
	}
	if row == nil {
		t.Fatalf("`provider list --json` does not name %q:\n%v", providerName, rows)
	}
	for _, field := range []string{"tier", "health", "account_kind", "cost"} {
		if s, _ := row[field].(string); strings.TrimSpace(s) == "" {
			t.Errorf("registry field %q is empty in `provider list --json`; the row is %v", field, row)
		}
	}
	if lanes, _ := row["lanes"].(float64); lanes < 1 {
		t.Errorf("the provider has %v lanes, so nothing can route to it", row["lanes"])
	}
}

// drillGrant is step 3: the standing grant the daemon reads the
// credential under. Reports whether dispatch can be exercised.
//
// Two commands, in the order `cascade init` runs them on a machine that
// installs the daemon. The helper enrolment is what makes the grant
// possible; without it the grant refuses with elevation-required, which is
// correct and is what the harness asserts if it happens.
//
// The enrolment's own output says WHICH device key it got. On a host with
// no usable hardware or OS keystore it is a file-backed key, and the
// binary states plainly that this proves possession of a key file rather
// than local presence. That distinction is recorded in the drill's log,
// because it is the difference between the two kinds of evidence this
// ticket's journal may claim.
func drillGrant(t *testing.T, d drillEnv) bool {
	t.Helper()
	enrolled, enrollErr := d.runInteractive(t, "elevate-helper", "--enroll")
	if enrollErr != nil {
		t.Logf("dispatch leg not exercised: the elevation helper would not enrol.\n%s", strings.TrimSpace(enrolled))
		return false
	}
	if strings.Contains(enrolled, "FILE-backed") {
		t.Logf("elevation evidence: FILE-backed device key (no usable hardware or OS keystore on this host); " +
			"the dispatch below is proof of the ROUTING path, not of hardware-backed local presence")
	}
	out, err := d.runInteractive(t, "vault", "grant", "provider."+providerName+".key")
	if err == nil {
		return true
	}
	// The ONLY acceptable failure is the elevation one. Any other refusal
	// is a real defect wearing the same exit code.
	if !strings.Contains(out, "elevation-required") {
		t.Fatalf("`vault grant` failed for a reason that is not local presence: %v\n%s", err, out)
	}
	t.Logf("dispatch leg not exercised: issuing the standing grant needs local presence "+
		"(an enrolled elevation helper and a working authenticator), which this host could not supply.\n%s",
		strings.TrimSpace(out))
	return false
}

// drillDispatch is step 4: the conductor routes with nobody naming a
// provider.
func drillDispatch(t *testing.T, d drillEnv) {
	t.Helper()
	// NO --provider FLAG. That absence is the assertion: the router picks
	// the lane from quota, task class, sensitivity and health.
	out := d.mustRun(t, "run", "--task", "classify", "--input", "hello", "--json")
	envelope := decodeJSON(t, jsonTail(out))
	if ok, _ := envelope["ok"].(bool); !ok {
		t.Fatalf("`cascade run` with no --provider did not dispatch:\n%s", out)
	}
}

// drillUsage is step 5: the dispatch is accounted for, and no usage
// record carries a personal-table field.
func drillUsage(t *testing.T, d drillEnv, expectRows bool) {
	t.Helper()
	raw := jsonTail(d.mustRun(t, "provider", "usage", "--json"))
	for _, field := range personalFields {
		if strings.Contains(raw, `"`+field+`"`) {
			t.Errorf("a usage record carries the personal field %q; the plan says personal tables are "+
				"never tracked:\n%s", field, raw)
		}
	}
	if !expectRows {
		return
	}
	envelope := decodeJSON(t, raw)
	data, _ := envelope["data"].(map[string]any)
	rows, _ := data["rows"].([]any)
	if len(rows) == 0 {
		t.Fatalf("a dispatch happened and `provider usage --json` recorded nothing:\n%s", raw)
	}
	// Named, not merely counted. A row for some other provider would be
	// the same empty answer to "what did THIS provider cost".
	if !strings.Contains(raw, providerName) {
		t.Errorf("`provider usage --json` has rows but none names %q:\n%s", providerName, raw)
	}
}

// drillDoctor is step 6: the health check reads the same registry.
func drillDoctor(t *testing.T, d drillEnv) {
	t.Helper()
	out, err := d.run(t, "doctor")
	if err != nil {
		t.Fatalf("`cascade doctor` did not exit 0:\n%s", out)
	}
	line := ""
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "provider_health") {
			line = l
		}
	}
	if line == "" {
		t.Fatalf("`cascade doctor` has no provider_health check:\n%s", out)
	}
	if !strings.HasPrefix(strings.TrimSpace(line), "OK") {
		t.Errorf("provider_health is not green after a live verify: %s", strings.TrimSpace(line))
	}
	if !strings.Contains(line, "healthy") {
		t.Errorf("provider_health passed without reporting a healthy provider: %s", strings.TrimSpace(line))
	}
}

// jsonTail strips the leading warning lines a daemonless invocation
// prints before its envelope, so the document can be decoded.
//
// It slices from the first "{" rather than filtering known warnings: a
// filter has to be taught every new warning, and the one it has not been
// taught yet produces a decode failure that reads as a malformed
// envelope.
func jsonTail(out string) string {
	if i := strings.Index(out, "{"); i >= 0 {
		return out[i:]
	}
	return out
}
