//go:build integration

package acceptance

// Purpose (this file): the drill SCRIPT — the commands an operator runs,
//   in order, with what each must answer. Both lanes call it and differ
//   only in which endpoint answers.
//
// THE ELEVATED STEP IS A BOUNDARY, NOT A SKIP. Dispatch needs a standing
//   vault grant, and issuing one needs local presence: an enrolled
//   elevation helper and a working authenticator. A harness cannot supply
//   either. So the drill RUNS the grant, asserts that a refusal is the
//   elevation refusal and nothing else, and reports which half of the
//   script it therefore proved. A silent skip there would let a broken
//   dispatch path look like an unattended machine (R-14.282).
// SPORT: acceptance/j-s21-compat-sub (ADD) — P1-E10-W3-S21-T3.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// providerName is what the drill calls the provider it adds.
const providerName = "acceptance-compat"

// personalFields are the record columns the plan says are NEVER tracked
// in usage accounting ("personal tables never tracked"). Asserted against
// the raw JSON, so a field added under any nesting is caught.
var personalFields = []string{
	"prompt", "completion", "content", "message_text",
	"user_email", "user_name", "account_email",
}

// drillEnv is one drill run's isolated machine: its own HOME, its own
// cascade home, and the credential variable the target names.
type drillEnv struct {
	bin  string
	home string
	env  []string
}

// newDrillEnv builds the binary and a clean home for it.
func newDrillEnv(t *testing.T, target compatSubTarget) drillEnv {
	t.Helper()
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	env := []string{
		"HOME=" + home,
		"CASCADE_HOME=" + filepath.Join(home, ".cascade"),
		"CASCADE_NO_INPUT=1",
		"PATH=" + os.Getenv("PATH"),
	}
	// The credential travels as the NAMED variable, exactly as it will on
	// the real run. Its value is never logged and never becomes an
	// argument.
	if v := os.Getenv(target.KeyEnv); v != "" {
		env = append(env, target.KeyEnv+"="+v)
	}
	return drillEnv{bin: buildCascade(t, dir), home: home, env: env}
}

// run executes one cascade command and returns its combined output.
func (d drillEnv) run(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(d.bin, args...) //nolint:gosec // bin is built by this test.
	cmd.Env = d.env
	cmd.Dir = d.home
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// mustRun executes one command and fails the test if it does not exit 0.
func (d drillEnv) mustRun(t *testing.T, args ...string) string {
	t.Helper()
	out, err := d.run(t, args...)
	if err != nil {
		t.Fatalf("`cascade %s` failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

// runInteractive executes one command WITHOUT CASCADE_NO_INPUT.
//
// The elevated steps need it: that variable is a promise that nothing
// will block on a person, and the elevation gate honours it by refusing
// outright rather than by prompting. Dropping it for exactly these two
// commands is what an operator's own shell looks like; every other step
// in the drill keeps it, so a step that started prompting would hang the
// run instead of quietly passing.
func (d drillEnv) runInteractive(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(d.bin, args...) //nolint:gosec // bin is built by this test.
	cmd.Env = withoutNoInput(d.env)
	cmd.Dir = d.home
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// withoutNoInput copies env with CASCADE_NO_INPUT removed.
func withoutNoInput(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if strings.HasPrefix(kv, "CASCADE_NO_INPUT=") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// startDaemon runs `cascade daemon run` and waits for its socket to
// answer, returning the stop function.
func (d drillEnv) startDaemon(t *testing.T) func() {
	t.Helper()
	cmd := exec.Command(d.bin, "daemon", "run") //nolint:gosec // bin is built by this test.
	cmd.Env = d.env
	cmd.Dir = d.home
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting the daemon: %v", err)
	}
	stop := func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}
	for range 100 {
		if _, err := d.run(t, "status"); err == nil {
			return stop
		}
		time.Sleep(100 * time.Millisecond)
	}
	stop()
	t.Fatal("the daemon never answered `cascade status`")
	return func() {}
}

// runCompatSubDrill is the script.
func runCompatSubDrill(t *testing.T, target compatSubTarget) {
	t.Helper()
	d := newDrillEnv(t, target)
	d.mustRun(t, "init", "--yes", "--no-daemon")

	drillIntake(t, d, target)
	drillRegistry(t, d)
	granted := drillGrant(t, d)

	stop := d.startDaemon(t)
	defer stop()

	if granted {
		drillDispatch(t, d)
	}
	drillUsage(t, d, granted)
	drillDoctor(t, d)
}
