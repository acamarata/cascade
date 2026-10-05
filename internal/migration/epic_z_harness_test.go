// Purpose: isolate migration children and reject reachable platform custody.
// Inputs: temporary homes and a read-only command runner.
// Outputs: a sealed environment and guarded step execution.
// Constraints: no migration step runs before custody isolation is proven.
// SPORT: migration end-to-end acceptance.
package migration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func sealedEnv(t *testing.T) []string {
	t.Helper()
	base := t.TempDir()
	env := []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "DBUS_SESSION_BUS_ADDRESS=disabled:", "CASCADE_NO_INPUT=1"}
	for _, key := range []string{"HOME", "USERPROFILE", "CASCADE_HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_RUNTIME_DIR", "TMPDIR"} {
		path := filepath.Join(base, key)
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
		env = append(env, key+"="+path)
	}
	return env
}

func epicEnv(env []string, key string) string {
	for _, item := range env {
		if value, ok := strings.CutPrefix(item, key+"="); ok {
			return value
		}
	}
	return ""
}

func custodyPreflight(env []string, runner func(string, ...string) ([]byte, error)) error {
	return custodyPreflightFor(runtime.GOOS, env, runner)
}

func custodyPreflightFor(platform string, env []string, runner func(string, ...string) ([]byte, error)) error {
	switch platform {
	case "darwin":
		_, err := runner("/usr/bin/security", "default-keychain", "-d", "user")
		if err == nil {
			return fmt.Errorf("custody preflight: default keychain resolves")
		}
		if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() <= 0 {
			return fmt.Errorf("custody preflight: keychain predicate could not be evaluated: %w", err)
		}
	case "linux":
		if epicEnv(env, "DBUS_SESSION_BUS_ADDRESS") != "disabled:" || epicEnv(env, "XDG_RUNTIME_DIR") == "" {
			return fmt.Errorf("custody preflight: session bus is not disabled")
		}
		if _, err := os.Lstat(filepath.Join(epicEnv(env, "XDG_RUNTIME_DIR"), "bus")); !os.IsNotExist(err) {
			return fmt.Errorf("custody preflight: runtime bus may be reachable")
		}
	default:
		return fmt.Errorf("custody preflight: unsupported platform %s", platform)
	}
	return nil
}

func epicRunner(ctx context.Context, env []string) func(string, ...string) ([]byte, error) {
	return func(name string, args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, name, args...)
		cmd.Env = env
		return cmd.CombinedOutput()
	}
}

func runEpicZ(env []string, guard func([]string) error, steps []func() error) error {
	if err := guard(env); err != nil {
		return err
	}
	for _, step := range steps {
		if err := step(); err != nil {
			return err
		}
	}
	return nil
}

func epicDoctorReport(data []byte, rc int) error {
	var report struct {
		Data struct {
			Entries []struct {
				Name   string
				Result struct{ Status, Message string }
			}
		}
	}
	// The CLI emits the report envelope before its optional exit-error envelope.
	if err := json.NewDecoder(bytes.NewReader(data)).Decode(&report); err != nil {
		return err
	}
	if len(report.Data.Entries) == 0 {
		return fmt.Errorf("doctor returned no checks")
	}
	warnings, custody := 0, false
	for _, entry := range report.Data.Entries {
		switch entry.Result.Status {
		case "ok":
		case "warn":
			switch entry.Name {
			case "completion-gate-hooks", "hook-events", "provider_health", "subsystem_census":
				warnings++
			default:
				return fmt.Errorf("unexpected doctor warning: %s", entry.Name)
			}
		default:
			return fmt.Errorf("doctor %s outcome %q", entry.Name, entry.Result.Status)
		}
		if entry.Name == "secrets/keychain-reachable" && strings.Contains(entry.Result.Message, "file-vault") {
			custody = true
		}
	}
	if rc != 0 && (rc != 5 || warnings == 0) {
		return fmt.Errorf("doctor exit=%d with %d warnings", rc, warnings)
	}
	if !custody {
		return fmt.Errorf("doctor custody post-check did not name file-vault")
	}
	return nil
}
