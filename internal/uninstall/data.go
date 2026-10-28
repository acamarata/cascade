package uninstall

// Purpose: the data-class items --purge-data adds: the elevation key, the
//   config file and the data directory.
// Inputs: Deps.Paths, Deps.HelperDelete, Deps.GOOS.
// Outputs: dataItems.
// Constraints: on windows each data item refuses (KindUnsupported). The
//   data directory is emptied entry by entry through an os.Root, keeping
//   every backup path, and is removed only when nothing was kept. The
//   elevation key is the file-tier key under the data dir plus, when
//   HelperDelete is wired, the platform key behind the helper's own
//   presence check.
// SPORT: internal/uninstall data items/ADDED.

import (
	"context"
	"os"
	"path/filepath"

	"github.com/acamarata/cascade/pkg/cascade"
)

// platformKeyRemedy is the note when no platform key removal is wired.
func platformKeyRemedy(goos string) string {
	where := "the OS key store"
	if goos == "darwin" {
		where = "Keychain Access (service dev.cascade.elevation)"
	}
	return "a platform elevation key, if one is enrolled, is not removed by this build; delete it with " + where
}

// elevationKeyFile is the file-tier elevation key under the data dir.
const elevationKeyFile = "elevation.key"

// fileItem is a data-class regular file (config).
type fileItem struct {
	kind    Kind
	locator string
	d       Deps
}

func (f fileItem) Kind() Kind      { return f.kind }
func (f fileItem) Locator() string { return f.locator }
func (f fileItem) Owner() string   { return "cascade" }
func (f fileItem) Class() Class    { return ClassData }

// Remove removes the regular file at the locator.
func (f fileItem) Remove(_ context.Context, dryRun bool) (Delta, error) {
	delta := Delta{Kind: f.kind, Locator: f.locator}
	return delta, removeRegular(&delta, f.d, dryRun)
}

// removeRegular removes delta's locator when it is a regular file.
func removeRegular(delta *Delta, d Deps, dryRun bool) error {
	if err := guardPath(delta, d); err != nil {
		return err
	}
	exists, regular, err := lstatRegular(delta.Locator)
	switch {
	case err != nil:
		delta.Action = ActionKept
		return cascade.Wrapf(cascade.KindUnavailable, err, "uninstall: stat %s", delta.Locator)
	case !exists:
		delta.Action = ActionAbsent
		return nil
	case !regular:
		delta.Action, delta.Note = ActionKept, "not a regular file"
		return cascade.Newf(cascade.KindIntegrity, "uninstall: refused %s: not a regular file", delta.Locator)
	case dryRun:
		delta.Action = ActionWouldRemove
		return nil
	}
	if err := os.Remove(delta.Locator); err != nil {
		delta.Action = ActionKept
		return cascade.Wrapf(cascade.KindUnavailable, err, "uninstall: remove %s", delta.Locator)
	}
	delta.Action = ActionRemoved
	return nil
}

// keyItem is the elevation key.
type keyItem struct{ fileItem }

// Remove removes the file-tier key, then the platform key through
// HelperDelete when it is wired.
func (k keyItem) Remove(ctx context.Context, dryRun bool) (Delta, error) {
	delta := Delta{Kind: KindElevationKey, Locator: k.locator}
	if err := removeRegular(&delta, k.d, dryRun); err != nil {
		return delta, err
	}
	if k.d.HelperDelete == nil {
		note := platformKeyRemedy(k.d.GOOS)
		if delta.Action == ActionRemoved {
			note = "file-tier key removed; " + note
		}
		delta.Action, delta.Note = ActionKept, note
		return delta, nil
	}
	if dryRun {
		delta.Action, delta.Note = ActionWouldRemove, "and the platform key, after a presence check"
		return delta, nil
	}
	err := k.d.HelperDelete(ctx)
	switch {
	case err == nil:
		delta.Action = ActionRemoved
	case isNotFound(err):
	default:
		delta.Action, delta.Note = ActionKept, "the platform key was not removed"
		return delta, err
	}
	return delta, nil
}

// dataDirItem is the data directory.
type dataDirItem struct{ fileItem }

// Remove empties the data dir except backups, then removes it if empty.
func (dd dataDirItem) Remove(_ context.Context, dryRun bool) (Delta, error) {
	delta := Delta{Kind: KindDataDir, Locator: dd.locator}
	if err := guardPath(&delta, dd.d); err != nil {
		return delta, err
	}
	info, err := os.Lstat(dd.locator)
	switch {
	case os.IsNotExist(err):
		delta.Action = ActionAbsent
		return delta, nil
	case err != nil:
		delta.Action = ActionKept
		return delta, cascade.Wrapf(cascade.KindUnavailable, err, "uninstall: stat %s", dd.locator)
	case !info.IsDir():
		delta.Action, delta.Note = ActionKept, "not a real directory"
		return delta, cascade.Newf(cascade.KindIntegrity, "uninstall: refused %s: not a real directory", dd.locator)
	case dryRun:
		delta.Action, delta.Note = ActionWouldRemove, "backups inside it are kept"
		return delta, nil
	}
	kept, err := removeTree(dd.locator, dd.d)
	delta.Action = ActionRemoved
	if len(kept) > 0 {
		delta.Note = "kept backups: " + joinPaths(kept)
	}
	if err != nil {
		delta.Action = ActionKept
	}
	return delta, err
}

// dataItems is the elevation key, the config and the data dir.
func dataItems(d Deps) []Item {
	if d.Paths == nil {
		return nil
	}
	key := filepath.Join(d.Paths.DataDir(), elevationKeyFile)
	items := []fileItem{
		{kind: KindElevationKey, locator: key, d: d},
		{kind: KindConfig, locator: d.Paths.ConfigPath(), d: d},
		{kind: KindDataDir, locator: d.Paths.DataDir(), d: d},
	}
	out := make([]Item, 0, len(items))
	for _, it := range items {
		switch {
		case d.GOOS == "windows":
			out = append(out, dataRefusal(it))
		case it.kind == KindElevationKey:
			out = append(out, keyItem{it})
		case it.kind == KindDataDir:
			out = append(out, dataDirItem{it})
		default:
			out = append(out, it)
		}
	}
	return out
}

// dataRefusal is a windows data item: --purge-data is unsupported there.
func dataRefusal(it fileItem) Item {
	return dataKept{keptItem{delta: Delta{Kind: it.kind, Locator: it.locator, Action: ActionKept,
		Note: "--purge-data is not supported on windows"},
		err: cascade.New(cascade.KindUnsupported, "uninstall: --purge-data is not supported on windows")}}
}

// dataKept is a keptItem in the data class.
type dataKept struct{ keptItem }

func (dataKept) Class() Class { return ClassData }
