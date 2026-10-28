package uninstall

// Purpose: the executable and install-receipt items. A script-channel
//   executable is removed only when its sha256 equals its receipt entry;
//   every other channel keeps the binary and names the remedy. The receipt
//   is removed only once no receipt-listed file is left behind.
// Inputs: the execPlan from the receipt.
// Outputs: execItems.
// Constraints: removal goes through an os.Root at install_dir (Lstat at
//   the name, no symlink followed); a refused receipt removes no file and
//   keeps the receipt; windows keeps the running executable.
// SPORT: internal/uninstall executable items/ADDED.

import (
	"context"
	"os"
	"path/filepath"

	"github.com/acamarata/cascade/pkg/cascade"
)

// remedyModified is the remedy for a script install that drifted.
const remedyModified = NoteModified + "; rerun install.sh, then cascade uninstall"

// channelRemedy is the note for a binary a non-script channel owns.
var channelRemedy = map[string]string{
	"brew":         "installed by Homebrew; run: brew uninstall cascade",
	"oci":          "part of a container image; remove the image",
	"node-managed": "managed by its node; remove it through the node manager",
	"manual":       "installed by hand; delete it by hand",
}

// keptItem is an item kept by rule, optionally as an error.
type keptItem struct {
	delta Delta
	err   error
}

func (k keptItem) Kind() Kind      { return k.delta.Kind }
func (k keptItem) Locator() string { return k.delta.Locator }
func (k keptItem) Owner() string   { return "cascade" }
func (k keptItem) Class() Class    { return ClassInstall }
func (k keptItem) Remove(context.Context, bool) (Delta, error) {
	return k.delta, k.err
}

// execItem is one receipt-listed script-channel executable.
type execItem struct {
	plan execPlan
	name string
	want string
	d    Deps
}

func (e execItem) Kind() Kind      { return KindExecutable }
func (e execItem) Locator() string { return filepath.Join(e.plan.installDir, e.name) }
func (e execItem) Owner() string   { return "cascade" }
func (e execItem) Class() Class    { return ClassInstall }

// Remove removes the file when its digest matches the receipt.
func (e execItem) Remove(_ context.Context, dryRun bool) (Delta, error) {
	delta := Delta{Kind: KindExecutable, Locator: e.Locator()}
	if err := guardPath(&delta, e.d); err != nil {
		return delta, err
	}
	state, err := e.plan.fileStatus(e.name, e.want)
	switch {
	case err != nil:
		delta.Action = ActionKept
		return delta, err
	case state == fileAbsent:
		delta.Action = ActionAbsent
		return delta, nil
	case state == fileRefused:
		delta.Action, delta.Note = ActionKept, "not a regular file (a symlink is never followed)"
		return delta, cascade.Newf(cascade.KindIntegrity, "uninstall: refused %s: not a regular file", delta.Locator)
	case state == fileModified:
		delta.Action, delta.Note = ActionKept, remedyModified
		return delta, nil
	case e.plan.goos == "windows" && sameFile(filepath.Join(e.plan.resolved, e.name), e.plan.running):
		delta.Action, delta.Note = ActionKept, "running; delete it after this command exits"
		return delta, nil
	case dryRun:
		delta.Action = ActionWouldRemove
		return delta, nil
	}
	return delta, e.plan.removeFile(e.name, &delta)
}

// removeFile removes name under install_dir and records the outcome.
func (p execPlan) removeFile(name string, delta *Delta) error {
	root, err := os.OpenRoot(p.installDir)
	if err == nil {
		err = root.Remove(name)
		_ = root.Close()
	}
	if err != nil {
		delta.Action = ActionKept
		return cascade.Wrapf(cascade.KindUnavailable, err, "uninstall: remove %s", delta.Locator)
	}
	delta.Action = ActionRemoved
	return nil
}

// sameFile compares two paths after cleaning.
func sameFile(a, b string) bool { return b != "" && filepath.Clean(a) == filepath.Clean(b) }

// execItems is the executables then the receipt.
func execItems(d Deps) []Item {
	plan := buildExecPlan(d)
	receipt := receiptItem{plan: plan, d: d, locator: receiptPath(d)}
	switch {
	case plan.refused != nil:
		loc := plan.installDir
		if !filepath.IsAbs(loc) {
			loc = receipt.locator
		}
		return []Item{keptItem{delta: Delta{Kind: KindExecutable, Locator: loc, Action: ActionKept,
			Note: "install receipt refused; no executable is removed"}, err: plan.refused}, receipt}
	case !plan.present:
		return append(manualItems(plan), receipt)
	case plan.receipt.Channel != "script":
		return append(channelItems(plan), receipt)
	}
	var out []Item
	for _, base := range executableBases {
		name := exeName(base, d.GOOS)
		for _, f := range plan.receipt.Files {
			if f.Name == name {
				out = append(out, execItem{plan: plan, name: f.Name, want: f.SHA256, d: d})
			}
		}
	}
	return append(out, receipt)
}

// manualItems keeps the running binary when no receipt proves ownership.
func manualItems(p execPlan) []Item {
	if p.running == "" {
		return nil
	}
	return []Item{keptItem{delta: Delta{Kind: KindExecutable, Locator: p.running, Action: ActionKept,
		Note: "no install receipt; " + channelRemedy["manual"]}}}
}

// channelItems keeps every receipt-listed binary of a non-script channel.
func channelItems(p execPlan) []Item {
	note, ok := channelRemedy[p.receipt.Channel]
	if !ok {
		note = channelRemedy["manual"]
	}
	out := make([]Item, 0, len(p.receipt.Files))
	for _, f := range p.receipt.Files {
		out = append(out, keptItem{delta: Delta{Kind: KindExecutable,
			Locator: filepath.Join(p.installDir, f.Name), Action: ActionKept, Note: note}})
	}
	return out
}

// receiptPath is <cascade root>/install-receipt.json, or "" without paths.
func receiptPath(d Deps) string {
	if d.Paths == nil {
		return ""
	}
	return filepath.Join(d.Paths.Root(), receiptFileName)
}
