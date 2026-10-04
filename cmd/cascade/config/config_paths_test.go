package config

// Purpose: proves every `cascade config` verb refuses to run when the
//   config path cannot be resolved (AUD-034 W-1), instead of serving
//   defaults from an empty path.
// Inputs: a stub PathProvider whose resolution failed (empty paths plus a
//   ResolveErr, the shape cmd/cascade's lazyPaths has).
// Outputs: n/a (test-only).
// Constraints: no real HOME or environment is touched.
// SPORT: cmd/cascade/config (ADD, P1-CORE-16).

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// failedPaths is a PathProvider whose resolution failed: every accessor
// returns "" and ResolveErr carries the cause.
type failedPaths struct{ err error }

func (failedPaths) Root() string                         { return "" }
func (failedPaths) ConfigPath() string                   { return "" }
func (failedPaths) SocketPath() string                   { return "" }
func (failedPaths) DataDir() string                      { return "" }
func (failedPaths) LogDir() string                       { return "" }
func (failedPaths) StorageRoot(_ runtime.Profile) string { return "" }
func (f failedPaths) ResolveErr() error                  { return f.err }

func failedDepsRoot(t *testing.T, provider runtime.PathProvider) (*cobra.Command, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	root := &cobra.Command{Use: "cascade", SilenceUsage: true, SilenceErrors: true}
	for _, f := range []string{"json", "no-color"} {
		root.PersistentFlags().Bool(f, false, "")
	}
	root.PersistentFlags().BoolP("quiet", "q", false, "")
	root.PersistentFlags().BoolP("verbose", "v", false, "")
	root.AddCommand(NewConfigCmd(Deps{
		Paths:   provider,
		Getenv:  func(string) string { return "" },
		Clock:   runtime.NewFixedClock(time.Unix(0, 0)),
		Environ: func() []string { return nil },
	}))
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)
	return root, &out, &errOut
}

func TestConfigCommandsRefuseUnresolvedPath(t *testing.T) {
	cause := errors.New("runtime: resolve home directory: $HOME is not defined")
	cases := []struct {
		name     string
		provider runtime.PathProvider
	}{
		{"resolve error reported", failedPaths{err: cause}},
		{"empty path without a reported cause", failedPaths{}},
	}
	verbs := [][]string{
		{"get", "logging.level"},
		{"list"},
		{"validate"},
		{"set", "logging.level", "debug"},
		{"unset", "logging.level"},
		{"path"},
	}
	for _, tc := range cases {
		for _, args := range verbs {
			t.Run(tc.name+"/"+args[0], func(t *testing.T) {
				root, out, _ := failedDepsRoot(t, tc.provider)
				root.SetArgs(append([]string{"config"}, args...))
				err := root.ExecuteContext(context.Background())
				if err == nil {
					t.Fatalf("config %v succeeded on an unresolved path; stdout=%q", args, out.String())
				}
				kind, ok := cascade.KindOf(err)
				if !ok || kind != cascade.KindUnavailable {
					t.Fatalf("kind = %v (typed=%v), want KindUnavailable; err=%v", kind, ok, err)
				}
				if got := cascade.ExitCode(err); got != cascade.ExitUnavailable {
					t.Errorf("exit code = %d, want %d", got, cascade.ExitUnavailable)
				}
				if out.Len() != 0 {
					t.Errorf("stdout = %q, want nothing (no default value may be printed)", out.String())
				}
				if tc.provider.(failedPaths).err != nil && !strings.Contains(err.Error(), "$HOME is not defined") {
					t.Errorf("error %q does not name the resolution error", err)
				}
			})
		}
	}
}

// TestConfigGetSurfacesLoadWarning proves Load's warnings reach the operator:
// a group-writable config.toml prints exactly one warning on stderr and the
// value still resolves on stdout.
func TestConfigGetSurfacesLoadWarning(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("windows: config permissions are not checked")
	}
	home := t.TempDir()
	for _, k := range []string{"HOME", "USERPROFILE"} {
		t.Setenv(k, t.TempDir())
	}
	path := filepath.Join(home, "config.toml")
	if err := os.WriteFile(path, []byte("schema_version = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o660); err != nil {
		t.Fatal(err)
	}
	root, out, errOut := newTestRoot(t, home)
	root.SetArgs([]string{"config", "get", "schema_version"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("config get: %v", err)
	}
	// cobra's OutOrStderr honours SetOut first, so under a test root the
	// warning lands in the out buffer; in the real binary it is stderr.
	combined := out.String() + errOut.String()
	if n := strings.Count(combined, "group-writable"); n != 1 {
		t.Errorf("output = %q, want exactly one group-writable warning", combined)
	}
	if !strings.Contains(out.String(), "schema_version") {
		t.Errorf("stdout = %q, want the resolved value", out.String())
	}
}

// TestConfigValidateRefusesUnsafeConfig proves `config validate` applies
// Load's permission policy: a world-writable config.toml is refused with
// permission_denied and the fix, never reported valid.
func TestConfigValidateRefusesUnsafeConfig(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("windows: config permissions are not checked")
	}
	home := t.TempDir()
	for _, k := range []string{"HOME", "USERPROFILE"} {
		t.Setenv(k, t.TempDir())
	}
	path := filepath.Join(home, "config.toml")
	if err := os.WriteFile(path, []byte("schema_version = 1\n"), 0o600); err != nil || os.Chmod(path, 0o666) != nil {
		t.Fatal(err)
	}
	root, out, _ := newTestRoot(t, home)
	root.SetArgs([]string{"config", "validate"})
	err := root.ExecuteContext(context.Background())
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindPermissionDenied {
		t.Fatalf("validate err = %v (kind %v), want KindPermissionDenied; stdout=%q", err, kind, out.String())
	}
	if !strings.Contains(err.Error(), "chmod 600") || strings.Contains(out.String(), "is valid") {
		t.Errorf("err %q must carry the fix and stdout %q must not report valid", err, out.String())
	}
}
