package runtime

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"
)

// Purpose: the handler behind `cascade daemon logs [-f]`
//   (07-CLI-COMMAND-TREE §daemon noun; absorbs the earlier `cascade logs
//   tail` naming) — T-2 contract task 4.
//
// CASCADE-ALLOW: P1-E03-W1-S04-T2 DaemonLogsHandler is the real, fully
// implemented capability behind `cascade daemon logs`; only the cobra
// command mounting is deferred, because D/S-06.T2 (daemon lifecycle
// sprint, same wave) owns the cobra root's daemon subtree, which does
// not exist in this tree yet (06-FORGE-SPEC §5.19 allowed-fail pattern —
// same forward-stub shape as config_handlers.go's CASCADE-ALLOW). The
// handler is unit-tested directly against a log file path, independent
// of any CLI layer.
//
// Inputs: DaemonLogsOptions carries the log file path (LogFilePath,
//   logger.go, resolved via PathProvider — reading it never requires a
//   live daemon), Follow, and injected Out/Diag writers + Clock.
// Outputs: opts.Path's contents written to opts.Out; in follow mode, new
//   lines as they are appended, until ctx is cancelled or the file
//   disappears.
// Constraints: no inotify/FSEvents dependency (R-14.115 — no new
//   dependency; the contract also names this explicitly) — follow mode
//   polls via os.Stat + read. stdout=data / stderr=diag output contract
//   (D/S-06.T5): production callers pass os.Stdout/os.Stderr for
//   Out/Diag; tests always pass buffers. The file open goes through
//   openLogFile (daemon_logs_unix.go / daemon_logs_windows.go): a plain
//   os.Open on Windows omits FILE_SHARE_DELETE, so rotation.go's own
//   rename-away-and-reopen (the exact case followLoop below exists to
//   survive) would fail with ERROR_SHARING_VIOLATION while this handler
//   still holds the old file open — a real bug, not a test artifact.
// SPORT: runtime/logger (ADD, per T-2 sport_updates).

// DaemonLogsOptions carries DaemonLogsHandler's inputs.
type DaemonLogsOptions struct {
	// Path is the resolved log file path (LogFilePath(paths) in
	// production).
	Path string
	// Follow enables -f: after the initial read, poll for and emit new
	// lines until ctx is cancelled or the file disappears.
	Follow bool
	// Out receives the log content (stdout in production).
	Out io.Writer
	// Diag receives diagnostics — a missing/disappeared file — never log
	// content (stderr in production).
	Diag io.Writer
	// PollInterval is how often follow mode re-stats the file. <=0
	// defaults to 200ms. Follow mode has no size- or time-based business
	// logic of its own (it only decides "did the file grow"), so unlike
	// rotation.go it needs no injected Clock to stay deterministic —
	// tests drive it via a short PollInterval and ctx cancellation
	// instead (Art.7.3 governs values read INTO a decision, not a
	// polling cadence).
	PollInterval time.Duration
	// Ticker paces follow mode's poll loop, mirroring metrics_emitter.go's
	// Ticker seam. Production leaves this nil and gets a real
	// NewSystemTicker(PollInterval); tests inject a manually-driven fake so
	// each poll is triggered on demand instead of raced against a real
	// interval, which is what makes the rotation-gap grace period
	// (missingGraceTicks) provable by tick COUNT rather than by hoping a
	// real Sleep landed inside the right window under load.
	Ticker Ticker
}

// DaemonLogsHandler streams opts.Path to opts.Out. Without Follow, it
// reads the file to EOF and returns. A missing file is not an error —
// the daemon may never have run yet — it emits a diagnostic to opts.Diag
// and returns nil (matches the "does not require a live daemon" AC:
// there is nothing to read, not a failure).
func DaemonLogsHandler(ctx context.Context, opts DaemonLogsOptions) error {
	interval := opts.PollInterval
	if interval <= 0 {
		interval = 200 * time.Millisecond
	}

	f, err := openLogFile(opts.Path)
	if err != nil {
		if os.IsNotExist(err) {
			_, _ = fmt.Fprintf(opts.Diag, "runtime: daemon logs: no log file yet at %s\n", opts.Path)
			return nil
		}
		return &LogError{Field: "daemon.logs", Reason: fmt.Sprintf("open log file %s: %v", opts.Path, err)}
	}
	defer func() { _ = f.Close() }()

	offset, err := io.Copy(opts.Out, f)
	if err != nil {
		return &LogError{Field: "daemon.logs", Reason: fmt.Sprintf("read log file %s: %v", opts.Path, err)}
	}
	if !opts.Follow {
		return nil
	}
	return followLoop(ctx, opts, f, offset, interval)
}

// followLoop implements -f: poll via os.Stat, and whenever the file has
// grown past offset, read and emit the new bytes. It exits cleanly (nil
// error) on ctx cancellation, if the file disappears mid-poll, or if the
// file at opts.Path is rotated out from under the open handle — none of
// these are failures (docs/cli-reference/daemon.md documents all three
// as diagnostic-then-exit).
//
// BLOCKING FIX 1 (CR on P1-E03-W1-S04-T2): the prior version compared
// os.Stat(opts.Path).Size() against offset while continuing to read
// from the ORIGINAL *os.File. After rotation.go's rotateLocked renames
// the active file away and opens a fresh, smaller one at the same path,
// that fresh file's size is permanently <= offset (the reader's already
// consumed more bytes than the new file has ever contained), so the
// `info.Size() <= offset` branch was permanently true and follow mode
// silently stopped delivering new lines forever — no error, no
// diagnostic, no data — after the first rotation. Detecting rotation by
// size alone can never work: a rotated file legitimately starts small.
// Detecting it by IDENTITY (os.SameFile between the still-open handle
// and a fresh probe of the path) is the fix — same technique `tail -F`
// relies on.
//
// BLOCKING FIX 2 (Windows disappearance-vs-rotation): FIX 1's original
// shape called plain os.Stat(opts.Path) for the fresh side of that
// SameFile comparison. On Windows that is unreliable for exactly the
// disappearance case FIX 1 exists to tell apart from rotation. A deleted
// file with an open handle (this handler's own, kept open by the
// FILE_SHARE_DELETE fix in daemon_logs_windows.go) enters NTFS's
// delete-pending state, and os.Stat's Windows implementation resolves a
// bare path through GetFileAttributesEx first (Go's os/stat_windows.go:
// "Try GetFileAttributesEx first, because it is faster than CreateFile"),
// a lightweight metadata query that STILL SUCCEEDS against a
// delete-pending name and returns a FileInfo with its identity fields
// left zeroed. os.SameFile then has to re-resolve that identity lazily
// by re-opening the path (os/types_windows.go's loadFileId, itself a bare
// CreateFile), which for a delete-pending file fails outright (Windows
// refuses to open a name pending deletion, for any access or share mode)
// — and SameFile treats that failure as simply "not the same file", with
// no way to signal "could not tell". A deleted file therefore compares
// as a DIFFERENT file at the same path, which is exactly what a rotation
// looks like, and the wrong diagnostic fires. POSIX has no equivalent
// third state: unlink detaches the name immediately and unconditionally,
// so a later stat of the same path fails cleanly with ENOENT regardless
// of open handles; the unix side's plain os.Stat was already correct on
// its own terms, not accidentally so.
//
// The fix avoids bare path-Stat for the fresh side entirely: it probes
// the path with openLogFile (the same per-platform open every reader
// uses) and, on success, calls .Stat() on THAT HANDLE rather than on the
// path. A handle-based Stat always resolves identity via
// GetFileInformationByHandle on both platforms (os/stat_windows.go's
// statHandle path), never through the fragile-on-Windows
// GetFileAttributesEx shortcut, so it cannot return the zeroed-identity
// FileInfo that caused this. And critically, opening a delete-pending
// path by name fails outright on Windows — the same failure a truly
// missing path gives on both platforms — so a deletion now surfaces
// through the SAME "could not resolve the path at all" branch the
// missingGraceTicks logic already handles correctly, instead of falling
// through to the rotation branch. No runtime.GOOS branch was needed:
// openLogFile already carries the one genuine platform difference (the
// share flags), and everything downstream of it is identical on both
// platforms once the probe replaces the bare Stat.
//
// missingGraceTicks is how many CONSECUTIVE polls may fail to resolve the
// log path before follow mode calls it deleted. It exists to span the gap
// between a rotation's rename and its create, which are two syscalls with
// a real, observable window between them. Three polls is long enough for
// that window on a loaded machine and short enough that a genuinely
// deleted file is still reported within a few poll intervals.
const missingGraceTicks = 3

func followLoop(ctx context.Context, opts DaemonLogsOptions, f *os.File, offset int64, interval time.Duration) error {
	tick := opts.Ticker
	if tick == nil {
		tick = NewSystemTicker(interval)
	}
	defer tick.Stop()

	openInfo, err := f.Stat()
	if err != nil {
		return &LogError{Field: "daemon.logs", Reason: fmt.Sprintf("stat open log file %s: %v", opts.Path, err)}
	}

	// missing counts CONSECUTIVE failed path resolutions; see
	// pollFollowedFile for why a single one is not proof of deletion.
	missing := 0

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C():
			done, next, pollErr := pollFollowedFile(opts, f, openInfo, offset, &missing)
			if pollErr != nil {
				return pollErr
			}
			offset = next
			if done {
				return nil
			}
		}
	}
}

// pollFollowedFile runs one followLoop poll. It resolves whether
// opts.Path still names the file f has open by probing the path (see
// BLOCKING FIX 2 above for why a probe-open, not a bare Stat, is the
// reliable check), reports done=true with the matching diagnostic on
// either disappearance or rotation, and otherwise copies any newly
// appended bytes to opts.Out. missing is the consecutive-failure counter
// from followLoop, threaded through by pointer so it survives polls.
func pollFollowedFile(opts DaemonLogsOptions, f *os.File, openInfo os.FileInfo, offset int64, missing *int) (done bool, newOffset int64, err error) {
	var pathInfo os.FileInfo
	probe, probeErr := openLogFile(opts.Path)
	if probeErr == nil {
		pathInfo, probeErr = probe.Stat()
		_ = probe.Close()
	}
	if probeErr != nil {
		// A rotation is a RENAME followed by a CREATE, and those are two
		// separate syscalls: between them the path genuinely cannot be
		// opened. A poll that lands in that window looks exactly like a
		// deleted file, so waiting missingGraceTicks consecutive misses
		// before calling it gone costs nothing when the file really is
		// gone (it still exits, a few intervals later, with the same
		// diagnostic) and is the difference between correct and
		// incorrect when it is merely being rotated.
		*missing++
		if *missing <= missingGraceTicks {
			return false, offset, nil
		}
		_, _ = fmt.Fprintf(opts.Diag, "runtime: daemon logs: log file %s disappeared\n", opts.Path)
		return true, offset, nil
	}
	*missing = 0
	if !os.SameFile(openInfo, pathInfo) {
		_, _ = fmt.Fprintf(opts.Diag, "runtime: daemon logs: log file %s was rotated out from under the reader\n", opts.Path)
		return true, offset, nil
	}
	if pathInfo.Size() <= offset {
		return false, offset, nil
	}
	n, copyErr := io.Copy(opts.Out, f)
	if copyErr != nil {
		return false, offset, &LogError{Field: "daemon.logs", Reason: fmt.Sprintf("read log file %s: %v", opts.Path, copyErr)}
	}
	return false, offset + n, nil
}
