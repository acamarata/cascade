// Purpose: `cascade uninstall` acceptance [4], [6], [7] over a fake install in t.TempDir (fake unit installer,
// ForceFileVault custody, real files) with the real local elevation gate (fake key) in front of --purge-data.
// Constraints: no real HOME, keychain, launchd or systemd; fake values only.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/daemon/service"
	"github.com/acamarata/cascade/internal/elevation"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/internal/uninstall"
	"github.com/acamarata/cascade/pkg/cascade"
)

type uninstallFixture struct {
	top     string
	paths   fakeDaemonPaths
	custody secrets.Custody
	unit    string
	goos    string
	order   []string
	invs    int
}

// orderUnits, orderGate and orderCustody log each side effect in order.
type orderUnits struct{ f *uninstallFixture }

func (u orderUnits) Install(service.Config) (service.DeltaReport, error) {
	return service.DeltaReport{}, nil
}
func (u orderUnits) Uninstall(service.Config) (service.DeltaReport, error) {
	u.f.order = append(u.f.order, "remove unit")
	if err := os.Remove(u.f.unit); os.IsNotExist(err) {
		return service.DeltaReport{Action: service.ActionNotInstalled}, nil
	}
	return service.DeltaReport{Action: service.ActionRemoved}, nil
}

// uninstallNoKeychain stands in for the platform keychain tool and refuses.
func uninstallNoKeychain(context.Context, string, ...string) ([]byte, error) {
	return nil, cascade.New(cascade.KindUnavailable, "fake: no keychain in tests")
}

func newUninstallFixture(t *testing.T) *uninstallFixture {
	t.Helper()
	top := t.TempDir()
	f := &uninstallFixture{top: top, goos: "linux", paths: fakeDaemonPaths{root: filepath.Join(top, "home", ".cascade")}}
	f.unit = filepath.Join(top, "home", ".config", "systemd", "user", "cascade.service")
	for p, body := range map[string]string{f.unit: "fake unit\n", f.paths.ConfigPath(): "# fake config\n",
		filepath.Join(f.paths.DataDir(), "cascade.db"): "fake store\n"} {
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	c, err := secrets.SelectCustody(secrets.Config{Service: "cascade-uninstall-test", Dir: f.paths.DataDir(),
		ForceFileVault: true, Passphrase: "fake-test-passphrase-not-a-secret-0123456789", Runner: uninstallNoKeychain})
	if err != nil {
		t.Fatal(err)
	}
	f.custody = c
	for _, n := range []string{"FAKE_TOKEN_A", "FAKE_TOKEN_B"} {
		if err := c.Set(t.Context(), n, []byte("fake-value")); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

// deps wires the fixture; the gate is logged before it runs.
func (f *uninstallFixture) deps(env map[string]string, gate secrets.ElevationGate) uninstallDeps {
	return uninstallDeps{
		Getenv: func(k string) string { return env[k] }, GOOS: f.goos,
		Gate: orderGate{f: f, inner: gate},
		Inventory: func(ctx context.Context) ([]uninstall.Item, error) {
			f.invs++
			return uninstall.Inventory(ctx, uninstall.Deps{Paths: f.paths, Services: orderUnits{f},
				DaemonCfg: &service.Config{HomeDir: filepath.Join(f.top, "home")}, GOOS: f.goos,
				Custody: func() (secrets.Custody, error) { return orderCustody{Custody: f.custody, f: f}, nil }})
		},
		Confirm: func(*cobra.Command, string) (bool, error) { return false, nil },
	}
}

type orderGate struct {
	f     *uninstallFixture
	inner secrets.ElevationGate
}

func (g orderGate) Authorize(ctx context.Context, verb string) error {
	g.f.order = append(g.f.order, "gate "+verb)
	if g.inner == nil {
		return cascade.New(cascade.KindElevationRequired, "fake: no gate")
	}
	return g.inner.Authorize(ctx, verb)
}

type orderCustody struct {
	secrets.Custody
	f *uninstallFixture
}

func (c orderCustody) Delete(ctx context.Context, name string) error {
	c.f.order = append(c.f.order, "delete "+name)
	return c.Custody.Delete(ctx, name)
}

func execUninstall(t *testing.T, deps uninstallDeps, args ...string) (string, error) {
	t.Helper()
	globalFlags = GlobalFlags{}
	root := newRootCmd()
	if existing, _, err := root.Find([]string{"uninstall"}); err != nil || existing.Name() != "uninstall" {
		t.Fatalf("uninstall is not mounted on the root command: %v", err)
	} else {
		root.RemoveCommand(existing)
	}
	root.AddCommand(newUninstallCmd(deps))
	buf := &bytes.Buffer{}
	root.SetOut(buf)
	root.SetErr(buf)
	root.SetIn(strings.NewReader(""))
	root.SetArgs(append([]string{"uninstall"}, args...))
	err := root.Execute()
	return buf.String(), err
}

// digestTree lists every path, type and body digest under root, so a mismatch names what changed.
func digestTree(t *testing.T, root string) string {
	t.Helper()
	var b strings.Builder
	_ = filepath.WalkDir(root, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			t.Fatalf("walk %s: %v", p, err)
		}
		rel, _ := filepath.Rel(root, p)
		sum := ""
		if e.Type().IsRegular() {
			body, _ := os.ReadFile(p)
			d := sha256.Sum256(body)
			sum = hex.EncodeToString(d[:8])
		}
		fmt.Fprintf(&b, "%s|%s|%s\n", filepath.ToSlash(rel), e.Type(), sum)
		return nil
	})
	return b.String()
}

func TestPurgeDataRefusedWithoutElevation(t *testing.T) {
	skipWithoutLocalElevation(t)
	f := newUninstallFixture(t)
	g := newGateFixture(t, nil)
	g.gate.attest = func(elevation.ElevationKeystore, runtime.Clock, string, string, string) (rpc.Attestation, error) {
		return rpc.Attestation{}, cascade.New(cascade.KindPermissionDenied, "fake: the signer refused")
	}
	before := digestTree(t, f.top)
	_, err := execUninstall(t, f.deps(nil, g.gate), "--purge-data", "--yes")
	if k, _ := cascade.KindOf(err); err == nil || k != cascade.KindElevationRequired {
		t.Fatalf("purge with a failing signer = %v; want KindElevationRequired", err)
	}
	if after := digestTree(t, f.top); after != before {
		t.Fatalf("a refused purge changed the tree:\nbefore:\n%safter:\n%s", before, after)
	}
	if len(f.order) != 1 || f.order[0] != "gate "+purgeDataVerb {
		t.Fatalf("order = %v; want only the gate call", f.order)
	}
	_, err = execUninstall(t, f.deps(nil, nil), "--purge-data", "--yes")
	if k, _ := cascade.KindOf(err); err == nil || k != cascade.KindElevationRequired || digestTree(t, f.top) != before {
		t.Fatalf("purge with a refusing gate = %v; want a refusal that removes nothing", err)
	}
	nilGate := f.deps(nil, nil)
	nilGate.Gate = nil
	_, err = execUninstall(t, nilGate, "--purge-data", "--yes")
	if k, _ := cascade.KindOf(err); err == nil || k != cascade.KindElevationRequired || digestTree(t, f.top) != before {
		t.Fatalf("purge with a nil gate = %v; want a refusal that removes nothing", err)
	}
	// Positive control: the same gate with the real signer grants the verb,
	// and the gate call comes before the first removal.
	f.order = nil
	if _, err := execUninstall(t, f.deps(nil, newGateFixture(t, nil).gate), "--purge-data", "--yes"); err != nil {
		t.Fatalf("granted purge: %v", err)
	}
	if len(f.order) < 2 || f.order[0] != "gate "+purgeDataVerb || f.order[1] != "remove unit" {
		t.Fatalf("order = %v; want the gate before the first Remove", f.order)
	}
	if _, err := os.Stat(f.paths.DataDir()); !os.IsNotExist(err) {
		t.Fatalf("granted purge left the data dir: %v", err)
	}
}

func TestUninstallSecondRunConverges(t *testing.T) {
	f := newUninstallFixture(t)
	if _, err := execUninstall(t, f.deps(nil, nil), "--yes"); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if names, _ := f.custody.List(t.Context()); len(names) != 0 {
		t.Fatalf("vault entries left: %v", names)
	}
	out, err := execUninstall(t, f.deps(nil, nil), "--yes", "--json")
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if !strings.Contains(out, `"items"`) || strings.Contains(out, `"removed"`) || strings.Contains(out, `"kept"`) {
		t.Fatalf("second run output = %s; want every item absent", out)
	}
	if _, err := os.Stat(f.paths.ConfigPath()); err != nil {
		t.Fatalf("a default uninstall removed the config: %v", err)
	}
}

func TestUninstallNoInputRequiresYes(t *testing.T) {
	f := newUninstallFixture(t)
	before := digestTree(t, f.top)
	_, err := execUninstall(t, f.deps(map[string]string{"CASCADE_NO_INPUT": "1"}, nil))
	if k, _ := cascade.KindOf(err); err == nil || k != cascade.KindInvalidInput {
		t.Fatalf("no input without --yes = %v; want a hard KindInvalidInput error", err)
	}
	if f.invs != 0 || len(f.order) != 0 || digestTree(t, f.top) != before {
		t.Fatalf("refusal ran inventory %d times, order %v", f.invs, f.order)
	}
}

func TestUninstallDryRunAndDeclinedConfirmRemoveNothing(t *testing.T) {
	f := newUninstallFixture(t)
	before := digestTree(t, f.top)
	out, err := execUninstall(t, f.deps(nil, nil), "--dry-run", "--purge-data")
	if err != nil || !strings.Contains(out, "would-remove") || digestTree(t, f.top) != before {
		t.Fatalf("dry run = %q, %v", out, err)
	}
	_, err = execUninstall(t, f.deps(nil, nil))
	if k, _ := cascade.KindOf(err); err == nil || k != cascade.KindPermissionDenied || digestTree(t, f.top) != before {
		t.Fatalf("declined confirm = %v", err)
	}
	deps := f.deps(nil, nil)
	deps.GOOS = "windows"
	if _, err := execUninstall(t, deps, "--purge-data", "--yes"); err == nil || f.invs != 2 {
		t.Fatalf("windows purge = %v (inventories %d); want a refusal before the inventory", err, f.invs)
	}
}

// TestUninstallDryRunNeverWritesTheRealStore runs the production inventory
// (real store, real receipt path, fake HOME/CASCADE_HOME, a refusing
// keychain runner) over an unmigrated store and a vault with entries: a dry
// run and a declined run write nothing and never select the delete custody;
// a confirmed run selects it once and deletes the entries. The inventory uses
// the real GOOS: assertPurgeDryRun holds the per-OS --purge-data contract.
func TestUninstallDryRunNeverWritesTheRealStore(t *testing.T) {
	top, vaultDir := t.TempDir(), t.TempDir()
	home := filepath.Join(top, "home")
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("CASCADE_HOME", filepath.Join(home, ".cascade"))
	// An empty file is a valid, unmigrated SQLite database: opening it in
	// place would run every migration and write the data dir.
	db := filepath.Join(home, ".cascade", "data", "cascade.db")
	if err := os.MkdirAll(filepath.Dir(db), 0o700); err != nil || os.WriteFile(db, nil, 0o600) != nil {
		t.Fatalf("seed the store: %v", err)
	}
	vault, err := secrets.SelectCustody(secrets.Config{Service: "cascade-uninstall-test", Dir: vaultDir, ForceFileVault: true,
		Passphrase: "fake-test-passphrase-not-a-secret-0123456789", Runner: uninstallNoKeychain})
	if err != nil || vault.Set(t.Context(), "FAKE_TOKEN_A", []byte("fake-value")) != nil {
		t.Fatalf("seed the vault: %v", err)
	}
	deletes := 0
	list := func(string) (secrets.Custody, error) { return vault, nil }
	del := func(string) (secrets.Custody, error) { deletes++; return vault, nil }
	deps := uninstallDeps{Getenv: func(string) string { return "" }, GOOS: "linux", Inventory: uninstallInventory(list, del),
		Confirm: func(*cobra.Command, string) (bool, error) { return false, nil }}
	before := digestTree(t, top)
	out, err := execUninstall(t, deps, "--dry-run", "--purge-data")
	assertPurgeDryRun(t, goruntime.GOOS, out, err, filepath.Join(home, ".cascade"))
	if _, err := execUninstall(t, deps); err == nil {
		t.Fatal("a declined run succeeded")
	}
	if digestTree(t, top) != before || deletes != 0 {
		t.Fatalf("dry/declined runs wrote the tree or selected the delete custody (%d)", deletes)
	}
	if _, err := execUninstall(t, deps, "--yes"); err != nil || deletes != 1 {
		t.Fatalf("confirmed run = %v, delete selections %d", err, deletes)
	}
	if names, _ := vault.List(t.Context()); len(names) != 0 {
		t.Fatalf("vault entries left: %v", names)
	}
}
