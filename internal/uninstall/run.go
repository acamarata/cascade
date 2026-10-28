package uninstall

// Purpose: Run, which removes the inventoried items in order, plus the
//   path guard every path-based Remove calls first.
// Inputs: the items from Inventory and Options.
// Outputs: a Report with one Delta per item that ran, and a joined error
//   naming every item that failed.
// Constraints: data-class items run only with PurgeData. A failed item
//   does not stop the run. A path locator that is relative or a backup
//   path is refused before any filesystem call.
// SPORT: internal/uninstall Run/ADDED.

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Run removes items in order. Data-class items run only when o.PurgeData.
// It continues past a failed item and returns every failure joined, each
// naming its item's kind and locator.
func Run(ctx context.Context, items []Item, o Options) (Report, error) {
	var rep Report
	var errs []error
	for _, it := range items {
		if it.Class() == ClassData && !o.PurgeData {
			continue
		}
		if err := ctx.Err(); err != nil {
			errs = append(errs, cascade.Wrap(cascade.KindCanceled, err, "uninstall: canceled"))
			break
		}
		delta, err := it.Remove(ctx, o.DryRun)
		if delta.Kind == "" {
			delta.Kind, delta.Locator = it.Kind(), it.Locator()
		}
		rep.Items = append(rep.Items, delta)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s %s: %w", it.Kind(), it.Locator(), err))
		}
	}
	if len(errs) == 0 {
		return rep, nil
	}
	return rep, joinKinded(errs)
}

// joinKinded joins errs under the first error's kind so the CLI's exit
// code still reflects what went wrong.
func joinKinded(errs []error) error {
	kind := cascade.KindInternal
	if k, ok := cascade.KindOf(errs[0]); ok {
		kind = k
	}
	return cascade.Wrap(kind, errors.Join(errs...), "uninstall: some items were not removed")
}

// guardPath refuses delta's locator when it is relative or a backup path,
// marking the delta kept. Every path-based Remove calls it first.
func guardPath(delta *Delta, d Deps) error {
	switch {
	case !filepath.IsAbs(delta.Locator):
		delta.Action, delta.Note = ActionKept, "locator is not an absolute path"
		return cascade.Newf(cascade.KindInvalidInput, "uninstall: refused %q: not an absolute path", delta.Locator)
	case IsBackupPath(delta.Locator, d):
		delta.Action, delta.Note = ActionKept, "inside a backup location"
		return cascade.Newf(cascade.KindIntegrity, "uninstall: refused %s: it is inside a backup location", delta.Locator)
	}
	return nil
}
