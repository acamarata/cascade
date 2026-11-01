package runtime

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// TestConfigTargetKinds distinguishes invalid targets and dangling links from
// an absent optional config, through the shared check and Load path.
func TestConfigTargetKinds(t *testing.T) {
	isolateHome(t)
	t.Run("directory", func(t *testing.T) {
		assertConfigDirectoryTarget(t, t.TempDir())
	})
	t.Run("symlink to directory", func(t *testing.T) {
		skipUnlessUnix(t)
		link := filepath.Join(t.TempDir(), "config.toml")
		if err := os.Symlink(t.TempDir(), link); err != nil {
			t.Fatal(err)
		}
		assertConfigDirectoryTarget(t, link)
	})
	t.Run("dangling final link", func(t *testing.T) { testDanglingConfigLink(t, false) })
	t.Run("dangling directory link", func(t *testing.T) { testDanglingConfigLink(t, true) })
	t.Run("dangling directory link then dot-dot", func(t *testing.T) {
		skipUnlessUnix(t)
		dir := t.TempDir()
		link, missing := filepath.Join(dir, "cfgdir"), filepath.Join(dir, "missing")
		if err := os.Symlink(missing, link); err != nil {
			t.Fatal(err)
		}
		assertDanglingConfigTarget(t, link+string(os.PathSeparator)+".."+string(os.PathSeparator)+"config.toml", link, missing)
	})
	t.Run("absent file in real directory", func(t *testing.T) {
		assertAbsentConfigTarget(t, filepath.Join(t.TempDir(), "config.toml"))
	})
	t.Run("absent file under the /tmp symlink", func(t *testing.T) {
		skipUnlessUnix(t)
		assertAbsentConfigTarget(t, absentUnderSystemTmp(t))
	})
	t.Run("absent file inside live directory link", func(t *testing.T) {
		skipUnlessUnix(t)
		link := filepath.Join(t.TempDir(), "cfglink")
		if err := os.Symlink(t.TempDir(), link); err != nil {
			t.Fatal(err)
		}
		assertAbsentConfigTarget(t, filepath.Join(link, "config.toml"))
	})
}

// testDanglingConfigLink links config.toml (or, for directoryLink, the
// cfgdir holding it) to a missing target and expects a refusal naming it.
func testDanglingConfigLink(t *testing.T, directoryLink bool) {
	skipUnlessUnix(t)
	dir := t.TempDir()
	link, missing := filepath.Join(dir, "config.toml"), filepath.Join(dir, "missing.toml")
	if directoryLink {
		link, missing = filepath.Join(dir, "cfgdir"), filepath.Join(dir, "missing")+string(os.PathSeparator)
	}
	if err := os.Symlink(missing, link); err != nil {
		t.Fatal(err)
	}
	path := link
	if directoryLink {
		path = filepath.Join(link, "config.toml")
	}
	assertDanglingConfigTarget(t, path, link, missing)
}

// absentUnderSystemTmp names a config.toml that does not exist in a
// directory that does not exist directly under /tmp (a symlink to
// /private/tmp on macOS), spelled through /tmp itself whatever the test
// temporary root is. Nothing is created there.
func absentUnderSystemTmp(t *testing.T) string {
	t.Helper()
	if _, err := os.Stat("/tmp"); err != nil {
		t.Skipf("no /tmp on this host: %v", err)
	}
	// The test's own temporary root name is unique to this run.
	dir := filepath.Join("/tmp", "cascade-absent-"+strconv.Itoa(os.Getpid())+"-"+filepath.Base(filepath.Dir(t.TempDir())))
	if _, err := os.Lstat(dir); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("%s should not exist: %v", dir, err)
	}
	return filepath.Join(dir, "config.toml")
}

func assertConfigDirectoryTarget(t *testing.T, path string) {
	t.Helper()
	isolateHome(t)
	_, err := CheckConfigPermissions(path)
	assertConfigTargetError(t, err, cascade.KindInvalidInput, path)
	cfg, err := loadConfigTarget(path)
	assertConfigTargetError(t, err, cascade.KindInvalidInput, path)
	if cfg != nil {
		t.Error("Load returned a config for a directory target")
	}
}

func assertDanglingConfigTarget(t *testing.T, path, link, target string) {
	t.Helper()
	isolateHome(t)
	finding, err := CheckConfigPermissions(path)
	if err != nil || finding.Level != ConfigPermRefuse {
		t.Fatalf("CheckConfigPermissions = %+v, %v; want refuse", finding, err)
	}
	for _, name := range []string{path, link, target} {
		if !strings.Contains(finding.Reason, name) {
			t.Errorf("refusal %q does not name %q", finding.Reason, name)
		}
	}
	cfg, err := loadConfigTarget(path)
	assertConfigTargetError(t, err, cascade.KindPermissionDenied, link)
	if cfg != nil {
		t.Error("Load returned defaults for a dangling config link")
	}
}

func assertAbsentConfigTarget(t *testing.T, path string) {
	t.Helper()
	isolateHome(t)
	finding, err := CheckConfigPermissions(path)
	if err != nil || finding.Level != ConfigPermOK || finding.Reason != "no config file" {
		t.Fatalf("CheckConfigPermissions = %+v, %v; want ok, no config file", finding, err)
	}
	if cfg, err := loadConfigTarget(path); err != nil || cfg == nil {
		t.Fatalf("Load = %v, %v; want default config and nil error", cfg, err)
	}
}

func loadConfigTarget(path string) (*Config, error) {
	return Load(context.Background(), LoadOptions{
		Path: path, Getenv: func(string) string { return "" },
		Environ: func() []string { return nil }, Warn: func(string, ...interface{}) {},
	})
}

func assertConfigTargetError(t *testing.T, err error, want cascade.Kind, path string) {
	t.Helper()
	kind, typed := cascade.KindOf(err)
	if !typed || kind != want {
		t.Fatalf("error = %v (kind %v, typed %v); want %v", err, kind, typed, want)
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("error %q does not name %q", err, path)
	}
}
