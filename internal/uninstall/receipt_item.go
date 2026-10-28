package uninstall

// Purpose: the install-receipt item, removed last among install items and
//   only when no file it lists is left behind.
// Inputs: the execPlan and the receipt path under the cascade root.
// Outputs: receiptItem.
// Constraints: a refused receipt, or one listing a file that is kept
//   (modified, not regular, running on windows), stays as the record of
//   what remains. A non-regular receipt path is refused.
// SPORT: internal/uninstall receipt item/ADDED.

import (
	"context"
	"os"
	"path/filepath"

	"github.com/acamarata/cascade/pkg/cascade"
)

// receiptItem is the install receipt.
type receiptItem struct {
	plan    execPlan
	d       Deps
	locator string
}

func (r receiptItem) Kind() Kind      { return KindInstallReceipt }
func (r receiptItem) Locator() string { return r.locator }
func (r receiptItem) Owner() string   { return "cascade" }
func (r receiptItem) Class() Class    { return ClassInstall }

// Remove removes the receipt when nothing it lists remains.
func (r receiptItem) Remove(_ context.Context, dryRun bool) (Delta, error) {
	delta := Delta{Kind: KindInstallReceipt, Locator: r.locator}
	if err := guardPath(&delta, r.d); err != nil {
		return delta, err
	}
	exists, regular, err := lstatRegular(r.locator)
	switch {
	case err != nil:
		delta.Action = ActionKept
		return delta, cascade.Wrapf(cascade.KindUnavailable, err, "uninstall: stat %s", r.locator)
	case !exists:
		delta.Action = ActionAbsent
		return delta, nil
	case !regular:
		delta.Action, delta.Note = ActionKept, "not a regular file"
		return delta, cascade.Newf(cascade.KindIntegrity, "uninstall: refused %s: not a regular file", r.locator)
	}
	if note := r.keepReason(dryRun); note != "" {
		delta.Action, delta.Note = ActionKept, note
		return delta, nil
	}
	if dryRun {
		delta.Action = ActionWouldRemove
		return delta, nil
	}
	if err := os.Remove(r.locator); err != nil {
		delta.Action = ActionKept
		return delta, cascade.Wrapf(cascade.KindUnavailable, err, "uninstall: remove %s", r.locator)
	}
	delta.Action = ActionRemoved
	return delta, nil
}

// keepReason says why the receipt must stay, or "". The receipt runs after
// the executables, so on a real run any listed file still present means an
// executable was kept and the receipt stays as its record.
func (r receiptItem) keepReason(dryRun bool) string {
	p := r.plan
	switch {
	case !p.present:
		return "not readable as an install receipt"
	case p.refused != nil:
		return "install receipt refused; kept as the record of the install"
	case p.receipt.Channel != "script":
		return ""
	}
	for _, f := range p.receipt.Files {
		state, err := p.fileStatus(f.Name, f.SHA256)
		if err != nil || state == fileModified || state == fileRefused {
			return "it lists executables that are kept"
		}
		if state == fileMatches && (!dryRun || p.goos == "windows" && sameFile(filepath.Join(p.resolved, f.Name), p.running)) {
			return "it lists executables that are kept"
		}
	}
	return ""
}
