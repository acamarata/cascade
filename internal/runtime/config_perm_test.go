package runtime

// Purpose: proves Load and CheckConfigPermissions share one classification
//   of config.toml (EPIC Decision 4): refuse a world-writable, foreign-owned
//   or loosely-parented file, warn once on group-writable, stay quiet on
//   world-readable (a doctor-only warning), and report not_checked on
//   windows.
// Inputs: real files with real modes under t.TempDir(); fresh HOME,
//   USERPROFILE and CASCADE_HOME per test.
// Outputs: n/a (test-only).
// Constraints: ownership cases need root (they execute in the linux
//   container run) and skip by name elsewhere. Windows has no unix mode bits,
//   so the windows expectation is a runtime.GOOS-gated test in this file.
// SPORT: runtime/config (ADD, P1-CORE-16).

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// isolateHome points HOME, USERPROFILE and CASCADE_HOME at fresh temp dirs.
func isolateHome(t *testing.T) {
	t.Helper()
	for _, k := range []string{"HOME", "USERPROFILE", "CASCADE_HOME"} {
		t.Setenv(k, t.TempDir())
	}
}

// warnCollector records formatted Warn messages, safe for concurrent use.
type warnCollector struct {
	mu   sync.Mutex
	msgs []string
}

func (w *warnCollector) warn(format string, args ...interface{}) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.msgs = append(w.msgs, fmt.Sprintf(format, args...))
}

func (w *warnCollector) all() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.msgs...)
}

// permCase describes one on-disk arrangement and both verdicts on it.
type permCase struct {
	name      string
	needsRoot bool
	setup     func(t *testing.T) string // returns the config.toml path
	level     ConfigPermLevel
	loadRefus bool
	loadWarns int
	substr    string // expected in the reason
}

// mkConfig writes config.toml with mode inside a directory of dirMode.
func mkConfig(t *testing.T, dirMode, fileMode os.FileMode) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "home")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, dirMode); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("schema_version = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, fileMode); err != nil {
		t.Fatal(err)
	}
	return path
}

// mkSymlinkConfig makes config.toml in a fresh directory of dirMode a
// symlink to target (made by the caller).
func mkSymlinkConfig(t *testing.T, dirMode os.FileMode, target string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "link")
	if err := os.Mkdir(dir, 0o700); err != nil || os.Chmod(dir, dirMode) != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "config.toml")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	return link
}

func chownOrFatal(t *testing.T, path string, uid int) {
	t.Helper()
	if err := os.Chown(path, uid, -1); err != nil {
		t.Fatal(err)
	}
}

func permCases() []permCase {
	const otherUID = 12345
	cfg := func(dir, file os.FileMode) func(*testing.T) string {
		return func(t *testing.T) string { return mkConfig(t, dir, file) }
	}
	return append([]permCase{
		{name: "0600 is ok", setup: cfg(0o700, 0o600), level: ConfigPermOK},
		{name: "0644 warns for doctor only", setup: cfg(0o700, 0o644), level: ConfigPermWarn, substr: "world-readable"},
		{name: "0660 loads with one warning", setup: cfg(0o700, 0o660), level: ConfigPermWarn, loadWarns: 1, substr: "group-writable"},
		{name: "0666 refused", setup: cfg(0o700, 0o666), level: ConfigPermRefuse, loadRefus: true, substr: "world-writable"},
		{name: "0602 refused", setup: cfg(0o700, 0o602), level: ConfigPermRefuse, loadRefus: true, substr: "world-writable"},
		{name: "0600 file in a 0777 directory refused", setup: cfg(0o777, 0o600), level: ConfigPermRefuse, loadRefus: true, substr: "chmod o-w"},
		{name: "0600 file in a sticky 1777 directory is ok", setup: cfg(0o777|os.ModeSticky, 0o600), level: ConfigPermOK},
		{name: "missing file is ok", setup: func(t *testing.T) string { return filepath.Join(t.TempDir(), "config.toml") }, level: ConfigPermOK},
		{name: "root-owned 0644 accepted", needsRoot: true, setup: cfg(0o755, 0o644), level: ConfigPermWarn, substr: "world-readable"},
		{name: "file owned by another uid refused", needsRoot: true, level: ConfigPermRefuse, loadRefus: true, substr: "chown",
			setup: func(t *testing.T) string {
				p := mkConfig(t, 0o755, 0o600)
				chownOrFatal(t, p, otherUID)
				return p
			}},
		{name: "0777 directory owned by another uid refused", needsRoot: true, level: ConfigPermRefuse, loadRefus: true, substr: "chmod o-w",
			setup: func(t *testing.T) string {
				p := mkConfig(t, 0o777, 0o600)
				chownOrFatal(t, filepath.Dir(p), otherUID)
				return p
			}},
		{name: "0755 directory owned by another uid refused", needsRoot: true, level: ConfigPermRefuse, loadRefus: true, substr: "chown",
			setup: func(t *testing.T) string {
				p := mkConfig(t, 0o755, 0o600)
				chownOrFatal(t, filepath.Dir(p), otherUID)
				return p
			}},
	}, symlinkPermCases()...)
}

// symlinkPermCases judge a symlinked config.toml by its target and by the
// directory holding every link in the chain.
func symlinkPermCases() []permCase {
	link := func(dir os.FileMode, target func(*testing.T) string) func(*testing.T) string {
		return func(t *testing.T) string { return mkSymlinkConfig(t, dir, target(t)) }
	}
	safeFile := func(t *testing.T) string { return mkConfig(t, 0o700, 0o600) }
	return []permCase{
		{name: "symlink to a 0600 file in a 0777 directory refused", level: ConfigPermRefuse, loadRefus: true, substr: "chmod o-w",
			setup: link(0o700, func(t *testing.T) string { return mkConfig(t, 0o777, 0o600) })},
		{name: "symlink in a 0777 dir to a 0600 file in a safe dir refused", level: ConfigPermRefuse, loadRefus: true, substr: "chmod o-w",
			setup: link(0o777, safeFile)},
		{name: "symlink in a sticky 1777 dir to a 0600 file in a safe dir is ok", level: ConfigPermOK,
			setup: link(0o777|os.ModeSticky, safeFile)},
		{name: "two-hop chain whose middle hop sits in a 0777 dir refused", level: ConfigPermRefuse, loadRefus: true, substr: "chmod o-w",
			setup: link(0o700, link(0o777, safeFile))},
		{name: "same-directory symlink is classified by its target", level: ConfigPermWarn, loadWarns: 1, substr: "real.toml",
			setup: func(t *testing.T) string {
				target := filepath.Join(filepath.Dir(mkConfig(t, 0o700, 0o600)), "real.toml")
				if err := os.WriteFile(target, []byte("schema_version = 1\n"), 0o600); err != nil || os.Chmod(target, 0o660) != nil {
					t.Fatal(err)
				}
				link := filepath.Join(filepath.Dir(target), "config.toml")
				if err := os.Remove(link); err != nil || os.Symlink("real.toml", link) != nil {
					t.Fatal(err)
				}
				return link
			}},
	}
}

func osIsWindows() bool { return goruntime.GOOS == "windows" }

func skipUnlessUnix(t *testing.T) {
	t.Helper()
	if goruntime.GOOS == "windows" {
		t.Skip("windows: no unix mode bits; see TestCheckConfigPermissionsWindows")
	}
}

func skipUnlessRoot(t *testing.T, c permCase) {
	t.Helper()
	if c.needsRoot && os.Geteuid() != 0 {
		t.Skipf("%s needs root (chown to another uid); it executes in the linux container run", c.name)
	}
}

func TestLoadRefusesUnsafeConfig(t *testing.T) {
	skipUnlessUnix(t)
	for _, c := range permCases() {
		t.Run(c.name, func(t *testing.T) {
			skipUnlessRoot(t, c)
			isolateHome(t)
			path := c.setup(t)
			before, statErr := os.Stat(path)
			w := &warnCollector{}
			cfg, err := Load(context.Background(), LoadOptions{
				Path: path, Getenv: func(string) string { return "" },
				Environ: func() []string { return nil }, Warn: w.warn,
			})
			if c.loadRefus {
				kind, typed := cascade.KindOf(err)
				if err == nil || !typed || kind != cascade.KindPermissionDenied {
					t.Fatalf("Load err = %v (kind %v, typed %v), want KindPermissionDenied", err, kind, typed)
				}
				if cfg != nil {
					t.Errorf("Load returned a config alongside the refusal")
				}
				if !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), c.substr) {
					t.Errorf("refusal %q must name the path %q and the fix (%q)", err, path, c.substr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			msgs := w.all()
			if len(msgs) != c.loadWarns {
				t.Fatalf("Warn messages = %q, want exactly %d", msgs, c.loadWarns)
			}
			if c.loadWarns == 1 && !strings.Contains(msgs[0], c.substr) {
				t.Errorf("warning %q does not mention %q", msgs[0], c.substr)
			}
			if statErr == nil {
				after, err := os.Stat(path)
				if err != nil || after.Mode() != before.Mode() {
					t.Errorf("Load changed the file mode: %v -> %v (%v)", before.Mode(), after.Mode(), err)
				}
			}
		})
	}
}

func TestCheckConfigPermissions(t *testing.T) {
	skipUnlessUnix(t)
	for _, c := range permCases() {
		t.Run(c.name, func(t *testing.T) {
			skipUnlessRoot(t, c)
			isolateHome(t)
			path := c.setup(t)
			got, err := CheckConfigPermissions(path)
			if err != nil {
				t.Fatalf("CheckConfigPermissions: %v", err)
			}
			if got.Level != c.level {
				t.Fatalf("level = %q (%s), want %q", got.Level, got.Reason, c.level)
			}
			if c.substr != "" && !strings.Contains(got.Reason, c.substr) {
				t.Errorf("reason %q lacks %q", got.Reason, c.substr)
			}
			// Shared classifier: Load must agree with the check, and its
			// message is the check's reason.
			w := &warnCollector{}
			_, loadErr := Load(context.Background(), LoadOptions{
				Path: path, Getenv: func(string) string { return "" },
				Environ: func() []string { return nil }, Warn: w.warn,
			})
			if (got.Level == ConfigPermRefuse) != (loadErr != nil) {
				t.Fatalf("check says %q but Load err = %v", got.Level, loadErr)
			}
			if loadErr != nil && !strings.Contains(loadErr.Error(), got.Reason) {
				t.Errorf("Load refusal %q does not carry the check's reason %q", loadErr, got.Reason)
			}
			if msgs := w.all(); len(msgs) == 1 && msgs[0] != "runtime: "+got.Reason {
				t.Errorf("Load warning %q differs from the check's reason %q", msgs[0], got.Reason)
			}
		})
	}
}

func TestCheckConfigPermissionsWindows(t *testing.T) {
	if goruntime.GOOS != "windows" {
		t.Skip("windows-only expectation (not_checked)")
	}
	isolateHome(t)
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("schema_version = 1\n"), 0o666); err != nil {
		t.Fatal(err)
	}
	got, err := CheckConfigPermissions(path)
	if err != nil || got.Level != ConfigPermNotChecked || got.Reason == "" {
		t.Fatalf("got %+v, %v; want not_checked with a reason", got, err)
	}
	if _, err := Load(context.Background(), LoadOptions{Path: path, Getenv: func(string) string { return "" }, Environ: func() []string { return nil }}); err != nil {
		t.Errorf("Load on windows must not refuse: %v", err)
	}
}
