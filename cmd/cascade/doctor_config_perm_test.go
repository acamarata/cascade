// Purpose: proves the config-permissions doctor check reports exactly what
// runtime.CheckConfigPermissions says (refuse, warn, ok, not_checked), over
// both injected findings and real files, and that `cascade daemon run`
// keeps the typed Kind of a refused config instead of re-wrapping it.
// Constraints: files live under t.TempDir(); unix mode bits do not exist on
// windows, where those cases skip and not_checked is asserted instead.
// SPORT: cmd/cascade/doctor (ADD, config-permissions tests).
package main

import (
	"context"
	"errors"
	"os"
	goruntime "runtime"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/doctor"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// TestDoctorConfigPermissions maps every level the classifier reports, then
// repeats the mapping over real files so the check and the classifier are
// shown to be wired together, not merely each correct alone.
func TestDoctorConfigPermissions(t *testing.T) {
	levels := []struct {
		level runtime.ConfigPermLevel
		want  doctor.Status
	}{
		{runtime.ConfigPermRefuse, doctor.StatusError},
		{runtime.ConfigPermWarn, doctor.StatusWarn},
		{runtime.ConfigPermOK, doctor.StatusOK},
		{runtime.ConfigPermNotChecked, doctor.StatusOK},
	}
	for _, tc := range levels {
		check := configPermissionsCheck{path: "/c/config.toml", classify: func(string) (runtime.ConfigPermFinding, error) {
			return runtime.ConfigPermFinding{Level: tc.level, Reason: "reason-" + string(tc.level)}, nil
		}}
		res := runCheck(t, check)
		if res.Status != tc.want || !strings.Contains(res.Detail, "reason-"+string(tc.level)) {
			t.Errorf("level %s = %+v, want %s carrying the classifier's reason", tc.level, res, tc.want)
		}
		if tc.level == runtime.ConfigPermNotChecked && !strings.Contains(res.Message, "not checked") {
			t.Errorf("not_checked message = %q, want it to say so", res.Message)
		}
	}
	failing := configPermissionsCheck{path: "/c", classify: func(string) (runtime.ConfigPermFinding, error) {
		return runtime.ConfigPermFinding{}, errors.New("stat denied")
	}}
	if res := runCheck(t, failing); res.Status != doctor.StatusError {
		t.Errorf("an uninspectable config = %+v, want StatusError", res)
	}
	if goruntime.GOOS == "windows" {
		paths := fakeDaemonPaths{root: t.TempDir()}
		if err := os.WriteFile(paths.ConfigPath(), []byte(""), 0o600); err != nil {
			t.Fatalf("write config: %v", err)
		}
		if res := runCheck(t, newConfigPermissionsDoctorCheck(paths)); res.Status != doctor.StatusOK || !strings.Contains(res.Message, "not checked") {
			t.Errorf("windows = %+v, want StatusOK reporting not checked", res)
		}
		return
	}
	for mode, want := range map[os.FileMode]doctor.Status{0o666: doctor.StatusError, 0o660: doctor.StatusWarn, 0o644: doctor.StatusWarn, 0o600: doctor.StatusOK} {
		paths := fakeDaemonPaths{root: t.TempDir()}
		if err := os.WriteFile(paths.ConfigPath(), []byte(""), 0o600); err != nil {
			t.Fatalf("write config: %v", err)
		}
		if err := os.Chmod(paths.ConfigPath(), mode); err != nil {
			t.Fatalf("chmod: %v", err)
		}
		if res := runCheck(t, newConfigPermissionsDoctorCheck(paths)); res.Status != want {
			t.Errorf("real config at %#o = %+v, want %s", mode, res, want)
		}
	}
}

// TestDaemonRunConfigPermissionKindPreserved: a refused config keeps its
// permission_denied Kind through `cascade daemon run`; malformed TOML, the
// positive control, is still classified invalid_input.
func TestDaemonRunConfigPermissionKindPreserved(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("no unix mode bits; the daemon refuses on windows tier-2")
	}
	run := func(contents string, mode os.FileMode) error {
		paths := fakeDaemonPaths{root: t.TempDir()}
		if err := os.WriteFile(paths.ConfigPath(), []byte(contents), 0o600); err != nil {
			t.Fatalf("write config: %v", err)
		}
		if err := os.Chmod(paths.ConfigPath(), mode); err != nil {
			t.Fatalf("chmod: %v", err)
		}
		deps := daemonDeps{Paths: paths, Getenv: func(string) string { return "" }, Environ: func() []string { return nil }, Executable: os.Executable}
		cmd := newDaemonCmd(deps)
		cmd.SetArgs([]string{"run"})
		cmd.SilenceUsage, cmd.SilenceErrors = true, true
		return cmd.ExecuteContext(context.Background())
	}
	if err := run("", 0o666); !cascade.HasKind(err, cascade.KindPermissionDenied) {
		t.Errorf("daemon run on a 0666 config = %v, want Kind permission_denied", err)
	}
	if err := run("this is not [ valid toml", 0o600); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Errorf("daemon run on malformed TOML = %v, want Kind invalid_input", err)
	}
}
