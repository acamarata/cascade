package main

// Purpose: the init adapters that reach OUTSIDE this process
//   (P1-E16-W4-S35-T6): the platform service installer, the elevation
//   helper enrollment, doctor's first-run lane, the two subcommand
//   hand-offs, the storage probe and the literal-secret guard.
// Constraints: Art.1 — each one calls the shipped implementation of its
//   step. Split from init_adapters.go for Art.10.3's 300-line cap; the
//   seam is "adapters over this process's own registries" there against
//   "adapters that start a process or touch the filesystem" here.
// SPORT: cmd/cascade init adapters (ADD) — P1-E16-W4-S35-T6.

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/acamarata/cascade/internal/doctor"
	"github.com/acamarata/cascade/internal/runtime"
	cascadeinit "github.com/acamarata/cascade/internal/runtime/init"
	"github.com/acamarata/cascade/pkg/cascade"
)

// initServiceInstaller installs the daemon through the same
// service.Installer `cascade daemon install` uses.
type initServiceInstaller struct {
	paths runtime.PathProvider
}

var _ cascadeinit.ServiceInstaller = initServiceInstaller{}

// Install runs the real platform installer.
func (i initServiceInstaller) Install(ctx context.Context) error {
	deps := productionDaemonDeps()
	cfg, err := daemonServiceConfig(ctx, deps)
	if err != nil {
		return err
	}
	_, err = deps.Installer.Install(cfg)
	return err
}

// initHelperEnroller runs the elevation helper's TOFU enrollment as a
// child process, so the enrollment the wizard performs is byte for byte
// the one `cascade elevate-helper --enroll` performs.
type initHelperEnroller struct{}

var _ cascadeinit.HelperEnroller = initHelperEnroller{}

// Enroll runs the subcommand and returns the fingerprint it printed.
func (initHelperEnroller) Enroll(ctx context.Context) (string, error) {
	out, err := runCascade(ctx, "elevate-helper", "--enroll")
	if err != nil {
		return "", err
	}
	return fingerprintIn(out), nil
}

// fingerprintIn extracts the key fingerprint from the enroll output.
//
// It reads the line rather than the whole output because the summary card
// shows one value; an enrollment that printed no recognisable fingerprint
// yields the empty string, and the card then omits the line rather than
// showing a paragraph of someone else's output.
func fingerprintIn(out string) string {
	for _, line := range strings.Split(out, "\n") {
		if i := strings.Index(line, "SHA256:"); i >= 0 {
			return strings.TrimSpace(line[i:])
		}
	}
	return ""
}

// initDoctorRunner runs doctor's first-run lane in process, over the same
// production registry `cascade doctor --first-run` builds.
type initDoctorRunner struct {
	paths runtime.PathProvider
}

var _ cascadeinit.DoctorRunner = initDoctorRunner{}

// FirstRun executes the first-run checks and renders their verdicts.
func (d initDoctorRunner) FirstRun(ctx context.Context) (string, error) {
	reg, err := productionCheckRegistry(ctx, d.paths, runtime.SystemClock{})
	if err != nil {
		return "", err
	}
	checks := reg.FirstRun()
	if len(checks) == 0 {
		// Absence is never a pass: a doctor that examined nothing must
		// not render as a clean bill of health (Art.1).
		return "", cascade.New(cascade.KindNotFound,
			"cascade init: no first-run diagnostic checks are registered")
	}
	report := doctor.Run(ctx, checks, doctor.RunOptions{FirstRunOnly: true, Clock: runtime.SystemClock{}})
	var b strings.Builder
	for _, e := range report.Entries {
		b.WriteString("   " + string(e.Result.Status) + "  " + e.Name)
		if e.Result.Message != "" {
			b.WriteString("  " + e.Result.Message)
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

// initSubprocess runs a cascade subcommand as a real child process, over
// the streams the parent command was given.
//
// The streams come from the cobra command rather than from the process
// globals: this package consumes an internal/output.Writer everywhere
// else, and a subprocess that reached past that to os.Stdout would be the
// one surface in the binary whose output a test could not capture.
type initSubprocess struct {
	in  io.Reader
	out io.Writer
	err io.Writer
}

var _ cascadeinit.Subprocess = initSubprocess{}

// Run executes `cascade <args...>`, passing the terminal through so the
// child can hold its own conversation with the operator — which is the
// whole reason these two steps hand off rather than calling in.
func (s initSubprocess) Run(ctx context.Context, args ...string) error {
	self, err := os.Executable()
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "cascade init: locate the cascade binary")
	}
	cmd := exec.CommandContext(ctx, self, args...) //nolint:gosec // self is this process's own path.
	cmd.Stdin, cmd.Stdout, cmd.Stderr = s.in, s.out, s.err
	if err := cmd.Run(); err != nil {
		return cascade.Wrapf(cascade.KindUnavailable, err, "cascade init: `cascade %s`", strings.Join(args, " "))
	}
	return nil
}

// runCascade runs a subcommand and captures its output.
func runCascade(ctx context.Context, args ...string) (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", cascade.Wrap(cascade.KindUnavailable, err, "cascade init: locate the cascade binary")
	}
	cmd := exec.CommandContext(ctx, self, args...) //nolint:gosec // self is this process's own path.
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), cascade.Wrapf(cascade.KindUnavailable, err,
			"cascade init: `cascade %s`: %s", strings.Join(args, " "), strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// initStorageProbe proves the cascade home is usable before any step
// writes to it.
type initStorageProbe struct{}

var _ cascadeinit.StorageProbe = initStorageProbe{}

// Probe writes a byte into the home and removes it again.
//
// A real write, not a stat: a directory that exists but is read-only, or
// sits on a full filesystem, passes every cheaper check and then fails at
// the first step that matters. Probing costs one file.
//
// When mayCreate is false — a --check run, which must write NOTHING —
// the probe walks up to the nearest directory that already exists and
// asks the question there instead. That is the same question: if the
// parent is writable, the home can be created under it.
func (initStorageProbe) Probe(_ context.Context, path string, mayCreate bool) error {
	target := path
	if mayCreate {
		if err := os.MkdirAll(target, 0o750); err != nil {
			return cascade.Wrapf(cascade.KindUnavailable, err, "create %s", target)
		}
	} else {
		var err error
		if target, err = nearestExisting(path); err != nil {
			return err
		}
	}
	probe := filepath.Join(target, ".cascade-init-probe")
	if err := os.WriteFile(probe, []byte("probe\n"), 0o600); err != nil {
		return cascade.Wrapf(cascade.KindUnavailable, err, "write to %s", target)
	}
	return os.Remove(probe)
}

// nearestExisting walks up from path to the first directory that exists.
//
// It cannot loop: filepath.Dir of a root returns that root, and the root
// of any mounted filesystem exists, so the walk terminates there at the
// latest.
func nearestExisting(path string) (string, error) {
	for {
		info, err := os.Stat(path)
		if err == nil && info.IsDir() {
			return path, nil
		}
		if err == nil {
			return "", cascade.Newf(cascade.KindConflict, "%s exists and is not a directory", path)
		}
		if !os.IsNotExist(err) {
			return "", cascade.Wrapf(cascade.KindUnavailable, err, "stat %s", path)
		}
		parent := filepath.Dir(path)
		if parent == path {
			return "", cascade.Newf(cascade.KindUnavailable, "no existing directory above %s", path)
		}
		path = parent
	}
}

// initSecretGuard refuses a literal secret typed at a storage prompt.
type initSecretGuard struct{}

var _ cascadeinit.LiteralSecretGuard = initSecretGuard{}

// Check refuses any value that is not a vault reference.
//
// The rule is stated positively — a connection setting must BE a
// reference — rather than as a pattern match for things that look like
// secrets. A denylist of secret shapes would pass the first credential
// format nobody thought of, and this prompt's answer goes into a
// plaintext config file.
func (initSecretGuard) Check(field, value string) error {
	if value == "" || isVaultReference(value) {
		return nil
	}
	return cascade.Newf(cascade.KindInvalidInput,
		"cascade init: %s must be a vault reference, not a literal value — "+
			"store it with `cascade vault set` and give the reference here", field)
}

// isVaultReference reports whether value is a reference rather than a
// literal.
func isVaultReference(value string) bool {
	for _, prefix := range []string{"vault://", "env:", "${"} {
		if strings.HasPrefix(value, prefix) {
			return true
		}
	}
	return false
}

// initWizardStepNames is the ordered list of step names `cascade init`
// renders, exported for the help text and the acceptance suite so the
// three cannot disagree about how many steps there are.
func initWizardStepNames() []string {
	names := make([]string, 0, int(cascadeinit.LastStep))
	for s := cascadeinit.StepPreflight; s <= cascadeinit.LastStep; s++ {
		names = append(names, s.String())
	}
	return names
}
