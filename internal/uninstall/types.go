package uninstall

// Purpose: the typed inventory vocabulary (contract:uninstall-primitives):
//   item kinds and classes, the per-item Delta, the Item interface, the
//   injected Deps and the Run options and report.
// Inputs: none (declarations only).
// Outputs: Kind, Class, Delta, Item, Deps, PluginLister, Options, Report.
// Constraints: Deps carries every external effect as a seam, so a test
//   never reaches the real HOME, keychain, launchd or systemd. The fields
//   after GOOS are additive to the contract: the widget unit has its own
//   installer type, and the executable and backup guards need the running
//   binary and the configured backup targets.
// SPORT: internal/uninstall primitives/ADDED.

import (
	"context"

	"github.com/acamarata/cascade/internal/daemon/service"
	"github.com/acamarata/cascade/internal/plugins"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/internal/update"
)

// Kind names what an Item is.
type Kind string

// The item kinds, in the order Inventory emits them.
const (
	KindDaemonUnit     Kind = "daemon-unit"
	KindWidgetAgent    Kind = "widget-agent"
	KindPlugin         Kind = "plugin"
	KindVaultEntry     Kind = "vault-entry"
	KindExecutable     Kind = "executable"
	KindInstallReceipt Kind = "install-receipt"
	KindElevationKey   Kind = "elevation-key"
	KindConfig         Kind = "config"
	KindDataDir        Kind = "data-dir"
)

// Class says when an Item is removed.
type Class string

const (
	// ClassInstall items are removed by default.
	ClassInstall Class = "install"
	// ClassData items are removed only with --purge-data.
	ClassData Class = "data"
)

// The Delta actions.
const (
	ActionRemoved     = "removed"
	ActionWouldRemove = "would-remove"
	ActionAbsent      = "absent"
	ActionKept        = "kept"
)

// NoteModified is the note on an executable whose digest no longer matches
// the receipt.
const NoteModified = "modified since install"

// Delta is what one Remove did, or would do under a dry run.
type Delta struct {
	Kind    Kind   `json:"kind"`
	Locator string `json:"locator"`
	Action  string `json:"action"`
	Note    string `json:"note,omitempty"`
}

// Item is one Cascade-owned install item.
type Item interface {
	Kind() Kind
	Locator() string
	Owner() string
	Class() Class
	// Remove removes the item. Remove(ctx, true) writes nothing.
	Remove(ctx context.Context, dryRun bool) (Delta, error)
}

// PluginLister lists installed plugins and removes one through its
// teardown hook (plugins.RemovePlugin in production).
type PluginLister interface {
	List(ctx context.Context) ([]string, error)
	Remove(ctx context.Context, teardown plugins.ProcessTeardown, name string) error
}

// Deps is every external input Inventory and the items need.
type Deps struct {
	Paths        runtime.PathProvider
	Services     service.Installer
	DaemonCfg    *service.Config
	WidgetCfg    *service.Config
	Plugins      PluginLister
	Teardown     plugins.ProcessTeardown
	Custody      func() (secrets.Custody, error)
	Receipt      func() (update.Receipt, bool, error)
	HelperDelete func(context.Context) error
	GOOS         string

	// Widgets manages the widget LaunchAgent; nil means
	// service.NewWidgetInstaller(). WidgetCfg's HomeDir, UID, Executable and
	// Runner are passed to its Uninstall.
	Widgets service.WidgetInstaller
	// Executable resolves the running binary; the receipt's install_dir must
	// be its directory. Nil refuses every executable item.
	Executable func() (string, error)
	// CustodyForDelete selects the custody entries are deleted through, on
	// the first non-dry vault Remove only (its selection may probe the store
	// with a transient write). It must be the same backend Custody listed
	// from. Nil deletes through the Custody that listed.
	CustodyForDelete func() (secrets.Custody, error)
	// BackupTargets lists the configured filesystem backup target roots. An
	// error makes every path read as a backup path (nothing path-based is
	// removed).
	BackupTargets func() ([]string, error)
}

// Options selects a dry run and whether data-class items run.
type Options struct {
	DryRun    bool
	PurgeData bool
}

// Report is every item's Delta, in run order.
type Report struct {
	Items []Delta `json:"items"`
}
