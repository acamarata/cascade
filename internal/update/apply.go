package update

// Purpose: run one self-update end to end over the separate steps:
//   Check -> FetchVerified -> Stage -> Swap -> Drain -> WaitVersion ->
//   receipt rewrite, restoring the previous set on any failure after Swap.
// Inputs: Deps.
// Outputs: a Result (the plan plus what was done), or the first error.
// Constraints: nothing is staged before every verification passed, the
//   downgrade rule is applied again to the downloaded set, and the fetched
//   archive must be the one Check selected. A script install runs under the
//   self-update lock from planning through the receipt rewrite. The previous dir is
//   <dataDir>/update/previous and holds the previous files plus
//   receipt.json. On windows there is no daemon: the old files are renamed
//   aside and the next start runs the new ones (no drain).
// SPORT: internal/update Apply/ADDED.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Result is Apply's answer (`self-update --json`).
type Result struct {
	CheckResult
	Updated         bool   `json:"updated"`
	DaemonSignalled bool   `json:"daemon_signalled"`
	PreviousDir     string `json:"previous_dir,omitempty"`
}

// PreviousDir is where Apply keeps the previous generation under dataDir.
func PreviousDir(dataDir string) string { return filepath.Join(dataDir, "update", "previous") }

// Apply performs the update the plan calls for. A script install is planned
// and changed only under the self-update lock (lock.go), so the receipt and
// install checks and the version comparison see what no other updater can
// change before the receipt is rewritten.
func Apply(ctx context.Context, d Deps) (Result, error) {
	ch, reason := ResolveChannel(d.Env)
	if ch != ChannelScript {
		return applyLocked(ctx, d, false)
	}
	if d.DataDir == "" {
		return Result{}, cascade.New(cascade.KindInternal, "update: apply needs a data dir")
	}
	lock, err := acquireUpdateLock(d.DataDir)
	if err != nil {
		return Result{CheckResult: CheckResult{Channel: ch, Reason: reason, Current: Ident{Version: d.Version}}}, err
	}
	defer lock.release()
	return applyLocked(ctx, d, true)
}

// applyLocked plans and, when the plan says update, installs and hands off.
// locked says whether the caller holds the self-update lock; a plan that
// resolves to a script install without it (the channel changed after Apply
// resolved it) is refused, never applied unlocked.
func applyLocked(ctx context.Context, d Deps, locked bool) (Result, error) {
	p, err := makePlan(ctx, d)
	res := Result{CheckResult: p.res}
	if err != nil || p.res.Action != ActionUpdate {
		return res, err
	}
	if !locked && p.res.Channel == ChannelScript {
		return res, cascade.New(cascade.KindConflict, "update: the install channel changed to script during self-update; nothing was changed, run `cascade self-update` again")
	}
	if d.DataDir == "" || d.Clock == nil {
		return res, cascade.New(cascade.KindInternal, "update: apply needs a data dir and a clock")
	}
	work, err := makeWorkDir(d.DataDir)
	if err != nil {
		return res, err
	}
	defer func() { _ = os.RemoveAll(work) }()
	pub, err := d.TrustRoot()
	if err != nil {
		return res, err
	}
	va, err := FetchVerified(ctx, d.Fetcher, d.Source, p.res.Latest.ReleaseTag, d.Platform, pub, work)
	if err != nil {
		return res, err
	}
	if _, err := downgradeRule(va.SignedVersion, d.Version, p.receipt); err != nil {
		return res, err
	}
	if va.SignedVersion != p.rel.Version || va.ArchiveSHA256 != p.rel.Entry.SHA256 {
		return res, integrity("the release changed between the check and the download; nothing was changed")
	}
	rec, err := install(va, p.receipt, PreviousDir(d.DataDir))
	if err != nil {
		return res, err
	}
	res.PreviousDir = rec.PreviousDir
	if res.DaemonSignalled, err = handOff(ctx, d, va.SignedVersion); err != nil {
		return res, restoreAfter(rec, signalledCause(err, res.DaemonSignalled))
	}
	if err := WriteReceipt(d.Env.receiptPath(), nextReceipt(p.receipt, va, d.Clock.Now())); err != nil {
		return res, restoreAfter(rec, signalledCause(err, res.DaemonSignalled))
	}
	res.Updated = true
	return res, nil
}

// makeWorkDir creates a private work directory under <dataDir>/update.
func makeWorkDir(dataDir string) (string, error) {
	base := filepath.Join(dataDir, "update")
	if err := os.MkdirAll(base, 0o700); err != nil {
		return "", unavailable(err, "create %s", base)
	}
	work, err := os.MkdirTemp(base, "work-")
	if err != nil {
		return "", unavailable(err, "create a work directory in %s", base)
	}
	return work, nil
}

// install stages and swaps the verified executables; Swap records the
// previous receipt beside the previous files before the first rename.
func install(va VerifiedArchive, current Receipt, previousDir string) (SwapRecord, error) {
	staged, err := Stage(va, current.InstallDir)
	if err != nil {
		return SwapRecord{}, err
	}
	return Swap(staged, current.InstallDir, previousDir, current)
}

// handOff drains a running daemon onto the new version (unix only).
func handOff(ctx context.Context, d Deps, version string) (bool, error) {
	if !drainsDaemon {
		return false, nil
	}
	dd := d.Daemon
	if dd.Clock == nil {
		dd.Clock = d.Clock
	}
	signalled, err := Drain(ctx, dd)
	if err != nil || !signalled {
		return signalled, err
	}
	return true, WaitVersion(ctx, dd, version)
}

// signalledCause adds, after a hand-off signal was sent, that the daemon may
// already run the new binary while the previous files are being restored,
// and the remedy. No second signal is ever sent.
func signalledCause(err error, signalled bool) error {
	if !signalled {
		return err
	}
	kind, ok := cascade.KindOf(err)
	if !ok {
		kind = cascade.KindUnavailable
	}
	return cascade.Wrap(kind, err, "update: the daemon was already signalled and may run the new binary "+
		"after the previous files are restored; run `cascade daemon restart` (or `cascade daemon stop` then `cascade daemon start`)")
}

// restoreAfter restores rec and returns cause with any restore failure.
func restoreAfter(rec SwapRecord, cause error) error {
	if rerr := Restore(rec); rerr != nil {
		return errors.Join(cause, restoreFailed(rerr))
	}
	kind, ok := cascade.KindOf(cause)
	if !ok {
		kind = cascade.KindUnavailable
	}
	return cascade.Wrap(kind, cause, "update: the previous version was restored")
}

// nextReceipt is the receipt for the newly installed release.
func nextReceipt(cur Receipt, va VerifiedArchive, now time.Time) Receipt {
	next := cur
	next.ReleaseTag, next.ArchiveName, next.ArchiveSHA256 = va.ReleaseTag, va.ArchiveName, va.ArchiveSHA256
	next.InstalledAt = now.UTC().Format(time.RFC3339)
	next.Files = nil
	for _, name := range va.Executables {
		sum, _ := fileDigest(filepath.Join(va.Dir, name))
		next.Files = append(next.Files, ReceiptFile{Name: name, SHA256: sum})
	}
	return next
}
