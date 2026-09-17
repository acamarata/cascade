//go:build integration

package acceptance

// Purpose (this file): Epic J's acceptance drill — a compat-subscription
//   provider added, live-verified, listed with real registry fields,
//   routed by the conductor with nobody naming it, accounted for, and
//   reported healthy by doctor. One script, run by two lanes.
//
// THE RESOLVER FAILS CLOSED. A drill with no endpoint configured is a
//   typed refusal naming the two variables to set, never a quiet fallback
//   to the rehearsal's local server wearing the drill's name. The
//   acceptance criterion this ticket exists for is "a REAL compat
//   subscription answered" (Art.2), and a harness that could satisfy
//   itself would report that criterion met on evidence nobody asked for.
//
// WHAT RUNS WHERE. The rehearsal (j_s21_rehearsal_test.go) runs wherever
//   the integration tag is on and proves the SCRIPT over a local endpoint.
//   The real drill runs only with a real subscription configured, and is
//   the 06 §7 owner prerequisite. The only difference between them is
//   which server answers.
//
// SPORT: acceptance/j-s21-compat-sub (ADD) — P1-E10-W3-S21-T3.

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// The environment the drill's target comes from (08 §2's CASCADE_* map).
// Read from the environment rather than a flag so a non-interactive run
// can supply it, and so CASCADE_NO_INPUT=1 changes nothing here — there is
// nothing to prompt for.
const (
	// envCompatSubBaseURL is the subscription's API root.
	envCompatSubBaseURL = "CASCADE_ACCEPTANCE_COMPAT_BASE_URL"
	// envCompatSubKeyEnv NAMES the variable holding the credential. It is
	// a name, not the value: `provider add --key-env` reads the variable
	// itself, so the secret never passes through this harness, never
	// reaches a test log, and never lands in a process argument list.
	envCompatSubKeyEnv = "CASCADE_ACCEPTANCE_COMPAT_KEY_ENV"
	// envCompatSubKind optionally pins the driver shape.
	envCompatSubKind = "CASCADE_ACCEPTANCE_COMPAT_KIND"
)

// compatSubTarget is the subscription the real drill runs against.
type compatSubTarget struct {
	// BaseURL is the API root `provider add --base-url` is given.
	BaseURL string
	// KeyEnv is the NAME of the variable holding the credential.
	KeyEnv string
	// Kind pins the driver shape, or is empty to let the probe decide.
	Kind string
	// Live says whether this target is the real subscription. The
	// rehearsal sets it false and is the only thing that may.
	Live bool
}

// resolveCompatSubTarget reads the target from getenv.
//
// Every failure is a TYPED error naming what to set. It is never a skip
// and never a local substitution: see this file's header for why that is
// the whole point of the harness rather than a detail of it.
func resolveCompatSubTarget(getenv func(string) string) (compatSubTarget, error) {
	if getenv == nil {
		return compatSubTarget{}, cascade.New(cascade.KindInternal,
			"compat-sub acceptance: the drill was built with no environment to read its target from")
	}
	base := strings.TrimSpace(getenv(envCompatSubBaseURL))
	if base == "" {
		return compatSubTarget{}, cascade.Newf(cascade.KindUnavailable,
			"compat-sub acceptance: no subscription is configured; set %s=https://… and %s=<name of the "+
				"variable holding the credential>. The local rehearsal proves the script and is not this drill",
			envCompatSubBaseURL, envCompatSubKeyEnv)
	}
	if !strings.HasPrefix(base, "https://") {
		// http:// is refused for the REAL drill specifically. The
		// rehearsal builds its target directly and is not resolved
		// through here, so this rule costs it nothing — and a drill that
		// accepted a plaintext endpoint could be pointed at a proxy and
		// still report the acceptance as met.
		return compatSubTarget{}, cascade.Newf(cascade.KindInvalidInput,
			"compat-sub acceptance: %s=%q is not https; the acceptance run sends a real credential",
			envCompatSubBaseURL, base)
	}
	keyEnv := strings.TrimSpace(getenv(envCompatSubKeyEnv))
	if keyEnv == "" {
		return compatSubTarget{}, cascade.Newf(cascade.KindInvalidInput,
			"compat-sub acceptance: %s is set but %s is not; the drill hands `provider add --key-env` a "+
				"variable NAME so the credential never passes through the harness, and cannot invent one",
			envCompatSubBaseURL, envCompatSubKeyEnv)
	}
	if strings.TrimSpace(getenv(keyEnv)) == "" {
		return compatSubTarget{}, cascade.Newf(cascade.KindUnavailable,
			"compat-sub acceptance: %s names %q, and %q is empty; `provider add --key-env` would "+
				"send no credential and the micro-verify would fail for the wrong reason",
			envCompatSubKeyEnv, keyEnv, keyEnv)
	}
	return compatSubTarget{
		BaseURL: base,
		KeyEnv:  keyEnv,
		Kind:    strings.TrimSpace(getenv(envCompatSubKind)),
		Live:    true,
	}, nil
}

// TestJ_S21_Acceptance_CompatSubEndToEnd is the real drill: the owner
// prerequisite, run against a live compat subscription.
//
// It SKIPS with the resolver's own message when no subscription is
// configured, rather than failing. A red test on every developer's machine
// and every CI run would be noise nobody reads, and the acceptance claim
// this ticket carries is tracked as a release prerequisite rather than as
// a permanently failing check (R-14.282, the same disposition Q/S-38.T5
// and S/S-42.T5 carry).
func TestJ_S21_Acceptance_CompatSubEndToEnd(t *testing.T) {
	target, err := resolveCompatSubTarget(os.Getenv)
	if err != nil {
		t.Skipf("%v", err)
	}
	runCompatSubDrill(t, target)
}

// TestTheDrillRefusesRatherThanSubstituting is the mutation-proof for the
// resolver: an unconfigured drill must produce a typed refusal naming both
// variables, not a target pointing anywhere.
func TestTheDrillRefusesRatherThanSubstituting(t *testing.T) {
	empty := func(string) string { return "" }
	got, err := resolveCompatSubTarget(empty)
	if err == nil {
		t.Fatalf("an unconfigured drill resolved a target %+v", got)
	}
	if got.Live {
		t.Error("a failed resolution still reported the target as live")
	}
	for _, want := range []string{envCompatSubBaseURL, envCompatSubKeyEnv} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %s: %v", want, err)
		}
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindUnavailable {
		t.Errorf("kind = %v (typed %t), want KindUnavailable", kind, ok)
	}
}

// TestTheDrillRefusesAPlaintextEndpoint keeps the real lane from being
// pointed at something that could read the credential in flight.
func TestTheDrillRefusesAPlaintextEndpoint(t *testing.T) {
	env := map[string]string{
		envCompatSubBaseURL: "http://example.invalid",
		envCompatSubKeyEnv:  "SOME_VAR",
		"SOME_VAR":          "a-value",
	}
	_, err := resolveCompatSubTarget(func(k string) string { return env[k] })
	if err == nil {
		t.Fatal("the drill accepted a plaintext endpoint for a run that sends a real credential")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInvalidInput {
		t.Errorf("kind = %v (typed %t), want KindInvalidInput", kind, ok)
	}
}

// TestTheDrillRefusesAKeyEnvThatNamesNothing is the case a resolver that
// only checked for non-empty strings would pass: the variable is named,
// and the variable is empty.
func TestTheDrillRefusesAKeyEnvThatNamesNothing(t *testing.T) {
	env := map[string]string{
		envCompatSubBaseURL: "https://example.invalid",
		envCompatSubKeyEnv:  "SOME_VAR",
	}
	_, err := resolveCompatSubTarget(func(k string) string { return env[k] })
	if err == nil {
		t.Fatal("the drill accepted a key-env naming an unset variable")
	}
	if !strings.Contains(err.Error(), "SOME_VAR") {
		t.Errorf("the refusal does not name the empty variable: %v", err)
	}
}

// buildCascade builds the shipped binary into dir.
//
// The drill drives the BINARY, not this repository's packages. Every
// acceptance criterion on this ticket is written as a command an operator
// runs, and a drill that called the intake package directly would prove
// the package while leaving the composition root — the part that was
// wrong in four of the W-4 gate's nine findings — unexercised.
func buildCascade(t *testing.T, dir string) string {
	t.Helper()
	bin := filepath.Join(dir, "cascade")
	build := exec.Command("go", "build", "-o", bin, "./cmd/cascade")
	build.Dir = moduleRoot(t)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building cascade: %v\n%s", err, out)
	}
	return bin
}

// moduleRoot walks up to the directory holding go.mod.
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the test's working directory")
		}
		dir = parent
	}
}

// decodeJSON parses one command's --json envelope payload.
func decodeJSON(t *testing.T, raw string) map[string]any {
	t.Helper()
	var envelope map[string]any
	if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
		t.Fatalf("the command answered something that is not JSON:\n%s", raw)
	}
	return envelope
}
