// Package plugins (effect_recovery.go): Purpose: the recorder identity every
// plugin.launch and plugin.call intent names, and RecoverPluginEffects, which
// settles the intents a dead recorder left open.
// Inputs: the effect log and the lock directory (<DataDir>/plugin-effects).
// Outputs: the number of intents recovery marked unknown-outcome.
// Constraints: a recorder is one process. It holds an advisory lock on
// <dir>/<token>.lock for its whole life and, before each launch's intent,
// writes <dir>/<launch-id>.launch naming its token (the effect log's handles
// do not carry an intent's Explain, so recovery reads the token from there).
// Recovery tries the token's lock without blocking: held means the recorder
// is alive and its intent is left alone; acquired or missing (lock or launch
// file) means it died, so the intent is marked unknown-outcome and the lock
// file removed. The lock primitive is injected (SetRecorderLock; cmd/cascade
// installs flock on unix): with none, no launch starts and recovery treats
// every recorder as alive. A recovery run also arms this process's recorder
// directory, so no launch records before recovery has run once.
// SPORT: internal/plugins plugin effect recovery (P1-PLG-03).
package plugins

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// The effect key prefixes a trusted plugin writes.
const (
	launchEffectPrefix = "plugin.launch:"
	callEffectPrefix   = "plugin.call:"
)

// hexToken is the shape of a recorder token and a launch id.
var hexToken = regexp.MustCompile(`^[0-9a-f]{32}$`)

// recorder is this process's identity and the locks it holds, per directory.
var recorder struct {
	mu    sync.Mutex
	token string
	dir   string
	held  map[string]*os.File
	try   func(f *os.File) (bool, error)
}

// SetRecorderLock installs the non-blocking advisory lock primitive (true:
// acquired; false: another holder has it) and returns a restore func.
func SetRecorderLock(try func(f *os.File) (bool, error)) (restore func()) {
	recorder.mu.Lock()
	previous := recorder.try
	recorder.try = try
	recorder.mu.Unlock()
	return func() {
		recorder.mu.Lock()
		recorder.try = previous
		recorder.mu.Unlock()
	}
}

// mintHex returns 128 bits of crypto/rand as lowercase hex.
func mintHex() (string, error) {
	var b [16]byte
	if _, err := cryptorand.Read(b[:]); err != nil {
		return "", cascade.Wrap(cascade.KindUnavailable, err, "plugins: minting a launch id")
	}
	return hex.EncodeToString(b[:]), nil
}

// armedRecorder returns the token and directory this process records under,
// taking (once per directory) the token's lock. It refuses when recovery has
// not run, no lock primitive is installed, or the lock cannot be held.
func armedRecorder() (token, dir string, err error) {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	if recorder.dir == "" || recorder.try == nil {
		return "", "", cascade.New(cascade.KindUnavailable,
			"plugins: plugin effect recovery has not run in this process, or no recorder lock is available; no plugin launch is recorded")
	}
	if recorder.token == "" {
		if recorder.token, err = mintHex(); err != nil {
			return "", "", err
		}
	}
	if recorder.held[recorder.dir] == nil {
		f, err := holdRecorderLock(recorder.dir, recorder.token, recorder.try)
		if err != nil {
			return "", "", err
		}
		if recorder.held == nil {
			recorder.held = map[string]*os.File{}
		}
		recorder.held[recorder.dir] = f
	}
	return recorder.token, recorder.dir, nil
}

// holdRecorderLock creates, locks and only then publishes <dir>/<token>.lock
// (lock a temporary name, rename it into place), so a published lock file is
// always held while its recorder lives and recovery may remove any it can
// take.
func holdRecorderLock(dir, token string, try func(*os.File) (bool, error)) (*os.File, error) {
	tmp := filepath.Join(dir, "."+token+".lock.tmp")
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "plugins: creating the recorder lock")
	}
	if ok, lerr := try(f); !ok || lerr != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return nil, cascade.Wrap(cascade.KindUnavailable, lerr, "plugins: the recorder lock is not held")
	}
	if err := os.Rename(tmp, filepath.Join(dir, token+".lock")); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "plugins: publishing the recorder lock")
	}
	return f, nil
}

// sweepDeadRecorders removes the owner files of launches with no pending
// intent and the lock files of recorders that are gone, so the directory
// does not grow with every process that ever launched a plugin.
func sweepDeadRecorders(dir string, pending map[string]bool) {
	launches, _ := filepath.Glob(filepath.Join(dir, "*.launch"))
	for _, p := range launches {
		if id := strings.TrimSuffix(filepath.Base(p), ".launch"); !pending[id] {
			if release, dead := recorderDead(dir, id); dead {
				release()
			}
		}
	}
	locks, _ := filepath.Glob(filepath.Join(dir, "*.lock"))
	tmps, _ := filepath.Glob(filepath.Join(dir, ".*.lock.tmp")) // a recorder that died before publishing
	for _, p := range append(locks, tmps...) {
		token := strings.TrimSuffix(strings.TrimSuffix(strings.TrimPrefix(filepath.Base(p), "."), ".tmp"), ".lock")
		if !hexToken.MatchString(token) {
			continue
		}
		if release, dead := lockDead(p); dead {
			release()
		}
	}
}

// recordLaunchOwner writes <dir>/<launchID>.launch naming token, before the
// launch's intent is begun.
func recordLaunchOwner(dir, launchID, token string) error {
	if err := runtime.WriteFileAtomic(filepath.Join(dir, launchID+".launch"), []byte(token), 0o600); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "plugins: recording the launch's recorder")
	}
	return nil
}

// RecoverPluginEffects marks unknown-outcome every pending plugin.launch and
// plugin.call intent whose recorder is gone, and returns how many it marked.
// A live recorder's intent is left untouched. A second run marks none.
func RecoverPluginEffects(ctx context.Context, e *audit.EffectLog, lockDir string) (int, error) {
	if e == nil || lockDir == "" {
		return 0, cascade.New(cascade.KindInvalidInput, "plugins: plugin effect recovery needs an effect log and a lock directory")
	}
	if err := os.MkdirAll(lockDir, 0o700); err != nil {
		return 0, cascade.Wrap(cascade.KindUnavailable, err, "plugins: creating the plugin effect lock directory")
	}
	pending, err := e.PendingEffects(ctx)
	if err != nil {
		return 0, err
	}
	marked, open := 0, map[string]bool{}
	for _, h := range pending {
		launchID, ok := effectLaunchID(h.Key)
		if !ok {
			continue
		}
		open[launchID] = true
		release, dead := recorderDead(lockDir, launchID)
		if !dead {
			continue
		}
		_, merr := e.MarkUnknownOutcome(ctx, h)
		release()
		if merr != nil && !errors.Is(merr, audit.ErrEffectNotPending) {
			return marked, merr
		}
		if merr == nil {
			marked++
			delete(open, launchID)
		}
	}
	sweepDeadRecorders(lockDir, open)
	armRecorderDir(lockDir)
	return marked, nil
}

// armRecorderDir makes dir this process's recorder directory.
func armRecorderDir(dir string) {
	recorder.mu.Lock()
	recorder.dir = dir
	recorder.mu.Unlock()
}

// effectLaunchID returns the launch id a plugin effect key names
// ("plugin.launch:<id>:<launch-id>" or "plugin.call:<id>:<launch-id>:<seq>").
func effectLaunchID(key string) (string, bool) {
	var rest string
	var want int
	switch {
	case strings.HasPrefix(key, launchEffectPrefix):
		rest, want = strings.TrimPrefix(key, launchEffectPrefix), 2
	case strings.HasPrefix(key, callEffectPrefix):
		rest, want = strings.TrimPrefix(key, callEffectPrefix), 3
	default:
		return "", false
	}
	parts := strings.Split(rest, ":")
	if len(parts) != want || !hexToken.MatchString(parts[1]) {
		return "", false
	}
	return parts[1], true
}

// recorderDead reports whether launchID's recorder is gone: its launch file
// or lock file is missing, or its lock can be taken. release (always safe to
// call) removes the dead recorder's lock file and drops the lock taken here.
func recorderDead(dir, launchID string) (release func(), dead bool) {
	noop := func() {}
	raw, err := os.ReadFile(filepath.Join(dir, launchID+".launch"))
	token := strings.TrimSpace(string(raw))
	if err != nil || !hexToken.MatchString(token) {
		return noop, errors.Is(err, os.ErrNotExist) || (err == nil && !hexToken.MatchString(token))
	}
	releaseLock, dead := lockDead(filepath.Join(dir, token+".lock"))
	if !dead {
		return noop, false
	}
	return func() {
		_ = os.Remove(filepath.Join(dir, launchID+".launch"))
		releaseLock()
	}, true
}

// lockDead reports whether the recorder lock at path is gone or can be
// taken (its recorder died); release removes it and drops the lock taken.
// With no lock primitive installed no recorder is proven dead.
func lockDead(path string) (release func(), dead bool) {
	noop := func() {}
	recorder.mu.Lock()
	try := recorder.try
	recorder.mu.Unlock()
	if try == nil {
		return noop, false
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return noop, errors.Is(err, os.ErrNotExist)
	}
	if ok, lerr := try(f); !ok || lerr != nil {
		_ = f.Close()
		return noop, false
	}
	return func() { _ = os.Remove(path); _ = f.Close() }, true
}

// recorded reports whether a terminal write sealed (or another process had
// already sealed) the record: a sealed record whose notification failed
// carries a sequence number with its error.
func recorded(r audit.Record, err error) bool {
	return err == nil || r.Seq != 0 || errors.Is(err, audit.ErrEffectNotPending)
}

// removeLaunchOwner drops a launch's owner file once none of its intents can
// still be pending.
func removeLaunchOwner(dir, launchID string) {
	_ = os.Remove(filepath.Join(dir, launchID+".launch"))
}
