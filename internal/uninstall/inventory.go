package uninstall

// Purpose: Inventory, the read-only listing of every Cascade-owned item in
//   removal order.
// Inputs: Deps.
// Outputs: the ordered Items: units, plugins, vault entries, executables
//   (helper and plugin before cascade), the receipt, then the data-class
//   elevation key, config and data dir.
// Constraints: performs no write (it lists, stats and hashes only). A
//   path-based item whose locator is a backup path is never listed. A
//   failed listing is an error: an inventory that silently omits a class
//   would read as "nothing to remove".
// SPORT: internal/uninstall Inventory/ADDED.

import (
	"context"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Inventory lists every Cascade-owned item in removal order.
func Inventory(ctx context.Context, d Deps) ([]Item, error) {
	if d.Paths == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "uninstall: no cascade paths were provided")
	}
	d = memoTargets(d)
	if d.BackupTargets != nil {
		if _, err := d.BackupTargets(); err != nil {
			return nil, cascade.Wrap(kindOr(err, cascade.KindUnavailable), err,
				"uninstall: list backup targets (needed to keep every backup)")
		}
	}
	items := unitItems(d)
	plugs, err := pluginItems(ctx, d)
	if err != nil {
		return nil, cascade.Wrap(kindOr(err, cascade.KindUnavailable), err, "uninstall: list plugins")
	}
	items = append(items, plugs...)
	vault, err := vaultItems(ctx, d)
	if err != nil {
		return nil, cascade.Wrap(kindOr(err, cascade.KindUnavailable), err, "uninstall: list vault entries")
	}
	items = append(items, vault...)
	items = append(items, execItems(d)...)
	items = append(items, dataItems(d)...)
	return withoutBackups(items, d), nil
}

// withoutBackups drops any item whose locator is a backup path.
func withoutBackups(items []Item, d Deps) []Item {
	out := items[:0]
	for _, it := range items {
		if !IsBackupPath(it.Locator(), d) {
			out = append(out, it)
		}
	}
	return out
}

// kindOr is err's kind, or def when it carries none.
func kindOr(err error, def cascade.Kind) cascade.Kind {
	if k, ok := cascade.KindOf(err); ok {
		return k
	}
	return def
}
