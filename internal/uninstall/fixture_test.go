package uninstall

// Purpose: the fake install every uninstall test runs against: fake units,
//   two fake plugins, a ForceFileVault custody with two indexed names plus
//   an entry in a second service's vault, and a receipt with three files,
//   all under one t.TempDir tree.
// Constraints: no real HOME, keychain, launchd or systemd; every value is
//   an obviously fake synthetic string.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"

	"github.com/acamarata/cascade/internal/daemon/service"
	"github.com/acamarata/cascade/internal/plugins"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/internal/update"
	"github.com/acamarata/cascade/pkg/cascade"
)

const fakePassphrase = "fake-test-passphrase-not-a-secret-0123456789"

// fakePaths is a PathProvider rooted in a temp dir.
type fakePaths struct{ root string }

func (p fakePaths) Root() string       { return p.root }
func (p fakePaths) ConfigPath() string { return filepath.Join(p.root, "config.toml") }
func (p fakePaths) SocketPath() string { return filepath.Join(p.root, "daemon.sock") }
func (p fakePaths) DataDir() string    { return filepath.Join(p.root, "data") }
func (p fakePaths) LogDir() string     { return filepath.Join(p.root, "logs") }
func (p fakePaths) StorageRoot(runtime.Profile) string {
	return filepath.Join(p.DataDir(), "storage")
}

// orderLog records every side effect in order.
type orderLog struct {
	mu  sync.Mutex
	ops []string
}

func (l *orderLog) add(op string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.ops = append(l.ops, op)
}

// fakeUnits removes a unit file the way the platform installers do.
type fakeUnits struct {
	path string
	log  *orderLog
}

func (u fakeUnits) Install(service.Config) (service.DeltaReport, error) {
	return service.DeltaReport{}, nil
}
func (u fakeUnits) Uninstall(service.Config) (service.DeltaReport, error) {
	u.log.add("remove " + filepath.Base(u.path))
	if err := os.Remove(u.path); os.IsNotExist(err) {
		return service.DeltaReport{Action: service.ActionNotInstalled}, nil
	} else if err != nil {
		return service.DeltaReport{}, err
	}
	return service.DeltaReport{Action: service.ActionRemoved}, nil
}

// fakeWidgets is the widget installer over the same file semantics.
type fakeWidgets struct{ fakeUnits }

func (w fakeWidgets) Install(service.WidgetConfig) (service.WidgetInstallReport, error) {
	return service.WidgetInstallReport{}, nil
}
func (w fakeWidgets) Uninstall(service.WidgetConfig) (service.DeltaReport, error) {
	return w.fakeUnits.Uninstall(service.Config{})
}
func (w fakeWidgets) Status(service.WidgetConfig) (service.WidgetStatusReport, error) {
	return service.WidgetStatusReport{}, nil
}

// fakePlugins keeps plugin records as files under dir; Remove runs the
// teardown first, like plugins.RemovePlugin.
type fakePlugins struct {
	dir string
	log *orderLog
}

func (p fakePlugins) List(context.Context) ([]string, error) {
	entries, err := os.ReadDir(p.dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names, nil
}

func (p fakePlugins) Remove(ctx context.Context, td plugins.ProcessTeardown, name string) error {
	p.log.add("remove plugin " + name)
	if td != nil {
		if err := td.Teardown(ctx, name); err != nil {
			return err
		}
	}
	if err := os.Remove(filepath.Join(p.dir, name)); os.IsNotExist(err) {
		return cascade.Newf(cascade.KindNotFound, "plugin %s is not installed", name)
	} else if err != nil {
		return err
	}
	return nil
}

// fixture is one fake install.
type fixture struct {
	top, home, installDir string
	paths                 fakePaths
	custody, other        secrets.Custody
	log                   *orderLog
	receipt               update.Receipt
	deps                  Deps
}

func newFixture(t *testing.T, goos string) *fixture {
	t.Helper()
	top := t.TempDir()
	if r, err := filepath.EvalSymlinks(top); err == nil {
		top = r // the running binary is symlink-resolved; macOS temp dirs sit behind /var and /tmp links
	}
	f := &fixture{top: top, home: filepath.Join(top, "home"), log: &orderLog{}}
	f.paths = fakePaths{root: filepath.Join(f.home, ".cascade")}
	f.installDir = filepath.Join(f.home, "bin")
	for _, dir := range []string{f.installDir, f.paths.DataDir(), filepath.Join(top, "plugins"), filepath.Join(top, "othersvc")} {
		mustMkdir(t, dir)
	}
	mustWrite(t, f.paths.ConfigPath(), "# fake config\n")
	mustWrite(t, filepath.Join(f.paths.DataDir(), "cascade.db"), "fake store\n")
	f.custody, f.other = newMemCustody("cascade-test-vault"), newMemCustody("other-service-vault")
	for _, n := range []string{"FAKE_TOKEN_A", "FAKE_TOKEN_B"} {
		mustSet(t, f.custody, n)
	}
	mustSet(t, f.other, "FAKE_OTHER_ENTRY")
	for _, n := range []string{"cascade-github", "cascade-pa"} {
		mustWrite(t, filepath.Join(top, "plugins", n), "fake plugin record\n")
	}
	f.writeReceipt(t, goos, "script")
	f.deps = f.newDeps(goos)
	return f
}

// newVaultFixture is newFixture over two real ForceFileVault custodies
// (scrypt-backed, so only the vault-focused tests pay for it).
func newVaultFixture(t *testing.T, goos string) *fixture {
	t.Helper()
	f := newFixture(t, goos)
	f.custody = fileVault(t, "cascade-test-vault", f.paths.DataDir())
	f.other = fileVault(t, "other-service-vault", filepath.Join(f.top, "othersvc"))
	for _, n := range []string{"FAKE_TOKEN_A", "FAKE_TOKEN_B"} {
		mustSet(t, f.custody, n)
	}
	mustSet(t, f.other, "FAKE_OTHER_ENTRY")
	return f
}

// newDeps wires the fakes; units only exist off windows.
func (f *fixture) newDeps(goos string) Deps {
	daemonPath := daemonUnitPath(goos, f.home)
	widgetPath := filepath.Join(f.home, "Library", "LaunchAgents", service.WidgetLabel+".plist")
	if goos != "windows" {
		for _, p := range []string{daemonPath, widgetPath} {
			_ = os.MkdirAll(filepath.Dir(p), 0o700)
			_ = os.WriteFile(p, []byte("fake unit\n"), 0o600)
		}
	}
	cfg := &service.Config{HomeDir: f.home}
	return Deps{
		Paths: f.paths, Services: fakeUnits{path: daemonPath, log: f.log}, DaemonCfg: cfg, WidgetCfg: cfg,
		Widgets: fakeWidgets{fakeUnits{path: widgetPath, log: f.log}},
		Plugins: fakePlugins{dir: filepath.Join(f.top, "plugins"), log: f.log},
		Custody: func() (secrets.Custody, error) { return f.custody, nil },
		Receipt: func() (update.Receipt, bool, error) {
			return f.receipt, exists(filepath.Join(f.paths.Root(), receiptFileName)), nil
		},
		GOOS: goos, Executable: func() (string, error) { return filepath.Join(f.installDir, exeName("cascade", goos)), nil },
		BackupTargets: func() ([]string, error) { return nil, nil },
	}
}

// writeReceipt installs the three executables and records their digests.
func (f *fixture) writeReceipt(t *testing.T, goos, channel string) {
	t.Helper()
	f.receipt = update.Receipt{SchemaVersion: 1, Channel: channel, ReleaseTag: "v0.0.1", InstallDir: f.installDir}
	for _, b := range executableBases {
		name := exeName(b, goos)
		body := "fake binary " + name + "\n"
		mustWrite(t, filepath.Join(f.installDir, name), body)
		f.receipt.Files = append(f.receipt.Files, update.ReceiptFile{Name: name, SHA256: digestOf(body)})
	}
	mustWrite(t, filepath.Join(f.paths.Root(), receiptFileName), "{\"fake\":\"receipt\"}\n")
}

// noKeychain stands in for the platform keychain tool and always refuses,
// so no test can reach the operator's real credential store.
func noKeychain(context.Context, string, ...string) ([]byte, error) {
	return nil, cascade.New(cascade.KindUnavailable, "fake: no keychain in tests")
}

func fileVault(t *testing.T, service, dir string) secrets.Custody {
	t.Helper()
	c, err := secrets.SelectCustody(secrets.Config{Service: service, Dir: dir, ForceFileVault: true,
		Passphrase: fakePassphrase, Runner: noKeychain})
	if err != nil {
		t.Fatalf("file vault: %v", err)
	}
	return c
}

func mustSet(t *testing.T, c secrets.Custody, name string) {
	t.Helper()
	if err := c.Set(t.Context(), name, []byte("fake-value")); err != nil {
		t.Fatalf("set %s: %v", name, err)
	}
}

func mustMkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
}

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	mustMkdir(t, filepath.Dir(path))
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func digestOf(body string) string {
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}

// treeDigest hashes every path, type and file body under root.
func treeDigest(t *testing.T, root string) string {
	t.Helper()
	h := sha256.New()
	err := filepath.WalkDir(root, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		_, _ = h.Write([]byte(filepath.ToSlash(rel) + "|" + e.Type().String() + "\n"))
		if e.Type().IsRegular() {
			b, rerr := os.ReadFile(p)
			if rerr != nil {
				return rerr
			}
			_, _ = h.Write(b)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("digest %s: %v", root, err)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// exists reports whether p exists (Lstat).
func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// mustInventory runs Inventory and fails the test on error.
func mustInventory(t *testing.T, d Deps) []Item {
	t.Helper()
	items, err := Inventory(t.Context(), d)
	if err != nil {
		t.Fatalf("Inventory: %v", err)
	}
	return items
}
