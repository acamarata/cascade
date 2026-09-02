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
//   Out/Diag; tests always pass buffers.
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

	f, err := os.Open(opts.Path)
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
// and a fresh stat of the path) is the fix — same technique `tail -F`
// relies on.
func followLoop(ctx context.Context, opts DaemonLogsOptions, f *os.File, offset int64, interval time.Duration) error {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	openInfo, err := f.Stat()
	if err != nil {
		return &LogError{Field: "daemon.logs", Reason: fmt.Sprintf("stat open log file %s: %v", opts.Path, err)}
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			pathInfo, err := os.Stat(opts.Path)
			if err != nil {
				_, _ = fmt.Fprintf(opts.Diag, "runtime: daemon logs: log file %s disappeared\n", opts.Path)
				return nil
			}
			if !os.SameFile(openInfo, pathInfo) {
				_, _ = fmt.Fprintf(opts.Diag, "runtime: daemon logs: log file %s was rotated out from under the reader\n", opts.Path)
				return nil
			}
			if pathInfo.Size() <= offset {
				continue
			}
			n, err := io.Copy(opts.Out, f)
			if err != nil {
				return &LogError{Field: "daemon.logs", Reason: fmt.Sprintf("read log file %s: %v", opts.Path, err)}
			}
			offset += n
		}
	}
}
