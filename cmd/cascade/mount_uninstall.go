// Purpose: registers the `uninstall` root mount and composes its
//
//	production inputs: the platform installers, the plugin store (with
//	each plugin's claude teardown), the host vault custody (listed through
//	the read-only selection; the process selection is taken on the first
//	real delete only), the
//	install receipt under the cascade root, the running executable and the
//	configured filesystem backup targets.
//
// Constraints: nothing is resolved while the command tree is built. The
//
//	inventory reads the store through a private snapshot (opening it runs
//	the migrator), and only Remove opens the real store, after the
//	confirmation and the gate; a fresh machine's inventory creates nothing. HelperDelete is nil:
//	internal/elevation exposes no caller-side request for the helper's
//	delete operation yet, so the platform elevation key is reported, not
//	removed.
//
// SPORT: cmd/cascade root mounts; uninstall production deps/ADDED.
package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	goruntime "runtime"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/backup"
	"github.com/acamarata/cascade/internal/daemon/service"
	"github.com/acamarata/cascade/internal/plugins"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/internal/uninstall"
	"github.com/acamarata/cascade/internal/update"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

var _ = registerRootMount(rootMount{Name: "uninstall", Order: 263, Mount: func(root *cobra.Command) {
	root.AddCommand(newUninstallCmd(productionUninstallDeps()))
}})

// productionUninstallDeps is the real environment.
func productionUninstallDeps() uninstallDeps {
	return uninstallDeps{Getenv: os.Getenv, GOOS: goruntime.GOOS,
		Gate: productionElevationGate(lazyPaths{}, os.Getenv), Inventory: uninstallInventory(readOnlyHostCustody, processHostCustody),
		Confirm: confirmUninstallOnStdin}
}

// uninstallInventory builds uninstall.Deps over the real environment; the
// custody selectors are injected so a test never reaches the real keychain.
func uninstallInventory(list, del custodySelector) func(context.Context) ([]uninstall.Item, error) {
	return func(ctx context.Context) ([]uninstall.Item, error) {
		paths := lazyPaths{}
		if err := paths.ResolveErr(); err != nil {
			return nil, cascade.Wrap(cascade.KindUnavailable, err, "uninstall: resolve cascade paths")
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, cascade.Wrap(cascade.KindUnavailable, err, "uninstall: resolve home directory")
		}
		exe, _ := os.Executable()
		cfg := &service.Config{HomeDir: home, Executable: exe, LogPath: runtime.LogFilePath(paths),
			UID: os.Getuid(), Runner: service.ExecRunner{}}
		store := uninstallStore{paths: paths}
		dataDir := paths.DataDir()
		return uninstall.Inventory(ctx, uninstall.Deps{
			Paths: paths, Services: service.NewInstaller(), DaemonCfg: cfg, WidgetCfg: cfg,
			Widgets: service.NewWidgetInstaller(), Plugins: store, GOOS: goruntime.GOOS,
			Custody:          func() (secrets.Custody, error) { return list(dataDir) },
			CustodyForDelete: func() (secrets.Custody, error) { return del(dataDir) },
			Receipt:          func() (update.Receipt, bool, error) { return readUninstallReceipt(paths.Root()) },
			Executable:       os.Executable,
			BackupTargets:    func() ([]string, error) { return store.backupTargets(ctx) },
		})
	}
}

// custodySelector selects a vault custody for a data dir.
type custodySelector func(dataDir string) (secrets.Custody, error)

// readOnlyHostCustody lists the host vault without any write.
func readOnlyHostCustody(dataDir string) (secrets.Custody, error) {
	return secrets.SelectCustodyReadOnly(hostCustodyConfig(dataDir))
}

// processHostCustody is the writable host vault; its selection probes the
// backend with a transient item, so it runs only on the first real delete.
func processHostCustody(dataDir string) (secrets.Custody, error) {
	return selectProcessCustody(hostCustodyConfig(dataDir))
}

// readUninstallReceipt reads the receipt; an absent one is (zero, false).
func readUninstallReceipt(root string) (update.Receipt, bool, error) {
	path := filepath.Join(root, "install-receipt.json")
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		return update.Receipt{}, false, nil
	}
	r, err := update.ReadReceipt(path)
	if err != nil {
		return update.Receipt{}, false, err
	}
	return r, true, nil
}

// uninstallStore reads the plugin store through a private snapshot and
// writes it only in Remove (after the confirmation and the gate).
type uninstallStore struct{ paths runtime.PathProvider }

// snapshotPaths is a PathProvider whose data dir is the snapshot dir.
type snapshotPaths struct {
	runtime.PathProvider
	dir string
}

func (p snapshotPaths) DataDir() string { return p.dir }

// dbPath is the real store database.
func (s uninstallStore) dbPath() string { return filepath.Join(s.paths.DataDir(), "cascade.db") }

// openSnapshot opens a private copy of the store, or ok=false when no
// database exists. Opening runs the store's migrator, so reading the real
// file in place would write the data dir (WAL files, migration backups);
// the copy takes those writes and is removed on close.
func (s uninstallStore) openSnapshot(ctx context.Context) (provider.Store, func(), bool, error) {
	src := s.dbPath()
	if _, err := os.Lstat(src); os.IsNotExist(err) {
		return nil, nil, false, nil
	}
	dir, err := os.MkdirTemp("", "cascade-uninstall-*")
	if err != nil {
		return nil, nil, false, cascade.Wrap(cascade.KindUnavailable, err, "uninstall: snapshot the store")
	}
	for _, suffix := range []string{"", "-wal"} {
		if err := copyRegular(src+suffix, filepath.Join(dir, "cascade.db"+suffix)); err != nil && (suffix == "" || !os.IsNotExist(err)) {
			_ = os.RemoveAll(dir)
			return nil, nil, false, cascade.Wrap(cascade.KindUnavailable, err, "uninstall: snapshot the store")
		}
	}
	store, closeStore, err := openPluginStore(ctx, snapshotPaths{PathProvider: s.paths, dir: dir}, runtime.NewSystemClock())
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, nil, false, err
	}
	return store, func() { closeStore(); _ = os.RemoveAll(dir) }, true, nil
}

// copyRegular copies a regular file (no symlink followed at src).
func copyRegular(src, dst string) error {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return cascade.Newf(cascade.KindIntegrity, "uninstall: %s is not a regular file", src)
	}
	in, err := os.Open(src) //nolint:gosec // the store under the resolved data dir
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // a fresh private temp dir
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// List names every installed plugin, read from a snapshot.
func (s uninstallStore) List(ctx context.Context) ([]string, error) {
	store, closeStore, ok, err := s.openSnapshot(ctx)
	if !ok {
		return nil, err
	}
	defer closeStore()
	recs, err := plugins.ListMetadata(ctx, store)
	names := make([]string, 0, len(recs))
	for _, r := range recs {
		names = append(names, r.Name)
	}
	return names, err
}

// Remove runs plugins.RemovePlugin on the real store. A nil teardown means
// the plugin's claude teardown over that store (`plugin remove`'s hook); a
// teardown that cannot be built refuses rather than orphaning artifacts.
func (s uninstallStore) Remove(ctx context.Context, td plugins.ProcessTeardown, name string) error {
	if _, err := os.Lstat(s.dbPath()); os.IsNotExist(err) {
		return cascade.Newf(cascade.KindNotFound, "plugin: %q is not installed", name)
	}
	store, closeStore, err := openPluginStore(ctx, s.paths, runtime.NewSystemClock())
	if err != nil {
		return err
	}
	defer closeStore()
	if td == nil {
		if td, err = claudeTeardown(pluginDeps{Paths: s.paths, Clock: runtime.NewSystemClock(), Getenv: os.Getenv}, store); err != nil {
			return err
		}
		if td == nil {
			return cascade.Newf(cascade.KindUnavailable,
				"uninstall: plugin %q kept: its teardown hook could not resolve the harness paths", name)
		}
	}
	_, err = plugins.RemovePlugin(ctx, store, td, name)
	return err
}

// backupTargets lists the configured filesystem backup target roots.
func (s uninstallStore) backupTargets(ctx context.Context) ([]string, error) {
	store, closeStore, ok, err := s.openSnapshot(ctx)
	if !ok {
		return nil, err
	}
	defer closeStore()
	recs, err := backup.ListTargets(ctx, store, backupRegistryNamespace)
	var roots []string
	for _, r := range recs {
		if r.Kind == backup.TargetKindFS && r.FSRoot != "" {
			roots = append(roots, r.FSRoot)
		}
	}
	return roots, err
}
