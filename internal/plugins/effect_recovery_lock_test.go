//go:build !windows

// Purpose: the recorder-lock and launch-owner halves of effect recovery:
// holding one lock per directory, dead-recorder verdicts, launch ids and the
// recovery refusals.
// SPORT: internal/plugins plugin effect recovery tests (P1-PLG-03).
package plugins

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// TestHoldRecorderLockPublishesOnlyAHeldLock: a lock file appears under its
// final name only once it is held, stays held while the recorder lives (a
// second taker is refused), and is takeable the moment the recorder closes.
func TestHoldRecorderLockPublishesOnlyAHeldLock(t *testing.T) {
	dir, token := t.TempDir(), strings.Repeat("c", 32)
	f, err := holdRecorderLock(dir, token, testFlock)
	if err != nil {
		t.Fatalf("holdRecorderLock: %v", err)
	}
	path := filepath.Join(dir, token+".lock")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the published lock file is missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "."+token+".lock.tmp")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the temporary lock name survived publishing: %v", err)
	}
	if _, dead := lockDead(path); dead {
		t.Fatal("a held recorder lock was judged dead")
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	release, dead := lockDead(path)
	if !dead {
		t.Fatal("a released recorder lock was not judged dead")
	}
	release()
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("releasing a dead recorder's lock left the file: %v", err)
	}
}

// TestHoldRecorderLockRefusals: every way the lock cannot be taken or
// published is a KindUnavailable refusal that leaves no lock file behind.
func TestHoldRecorderLockRefusals(t *testing.T) {
	token := strings.Repeat("d", 32)
	refuse := func(name string, try func(*os.File) (bool, error), setup func(dir string), want string) {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if setup != nil {
				setup(dir)
			}
			f, err := holdRecorderLock(dir, token, try)
			if f != nil || !cascade.HasKind(err, cascade.KindUnavailable) || !strings.Contains(err.Error(), want) {
				t.Fatalf("holdRecorderLock = %v, %v; want a KindUnavailable refusal naming %q", f, err, want)
			}
			if st, serr := os.Stat(filepath.Join(dir, token+".lock")); serr == nil && !st.IsDir() {
				t.Fatal("a refused hold published a lock file")
			}
			if _, serr := os.Stat(filepath.Join(dir, "."+token+".lock.tmp")); name != "stale temporary name" && serr == nil {
				t.Fatal("a refused hold left its temporary lock file")
			}
		})
	}
	refuse("lock held elsewhere", func(*os.File) (bool, error) { return false, nil }, nil, "not held")
	boom := errors.New("flock failed")
	refuse("lock primitive error", func(*os.File) (bool, error) { return true, boom }, nil, "not held")
	refuse("stale temporary name", testFlock, func(dir string) {
		if err := os.WriteFile(filepath.Join(dir, "."+token+".lock.tmp"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}, "creating the recorder lock")
	refuse("rename onto a directory", testFlock, func(dir string) {
		if err := os.MkdirAll(filepath.Join(dir, token+".lock", "occupied"), 0o700); err != nil {
			t.Fatal(err)
		}
	}, "publishing the recorder lock")
	if _, err := holdRecorderLock(filepath.Join(t.TempDir(), "missing"), token, testFlock); !cascade.HasKind(err, cascade.KindUnavailable) ||
		!strings.Contains(err.Error(), "creating the recorder lock") {
		t.Fatalf("holdRecorderLock in a missing directory = %v, want the creation refusal", err)
	}
	// The primitive's own error stays on the chain, so an operator sees why.
	if _, err := holdRecorderLock(t.TempDir(), token, func(*os.File) (bool, error) { return false, boom }); !errors.Is(err, boom) {
		t.Fatalf("the primitive's error was dropped from the chain: %v", err)
	}
}

// TestArmedRecorderHoldsOneLockPerDirectory: a recorder with no recovery run,
// or no lock primitive, refuses; otherwise it holds its token's lock once
// per directory and answers the same token and directory from then on.
func TestArmedRecorderHoldsOneLockPerDirectory(t *testing.T) {
	dir := t.TempDir()
	withRecorderState(t, "", func() {
		if _, _, err := armedRecorder(); !cascade.HasKind(err, cascade.KindUnavailable) || !strings.Contains(err.Error(), "recovery has not run") {
			t.Fatalf("armedRecorder before recovery = %v, want the unarmed refusal", err)
		}
	})
	withRecorderState(t, dir, func() {
		restore := SetRecorderLock(nil)
		_, _, err := armedRecorder()
		restore()
		if !cascade.HasKind(err, cascade.KindUnavailable) || !strings.Contains(err.Error(), "no recorder lock is available") {
			t.Fatalf("armedRecorder without a lock primitive = %v, want the refusal", err)
		}
		// A directory that cannot hold the lock refuses and records nothing held.
		armRecorderDir(filepath.Join(dir, "missing"))
		if _, _, err := armedRecorder(); !cascade.HasKind(err, cascade.KindUnavailable) {
			t.Fatalf("armedRecorder over a missing directory = %v, want KindUnavailable", err)
		}
		recorder.mu.Lock()
		held := len(recorder.held)
		recorder.mu.Unlock()
		if held != 0 {
			t.Fatalf("a refused hold was recorded as held (%d locks)", held)
		}
		armRecorderDir(dir)
		token, got, err := armedRecorder()
		if err != nil || got != dir || !hexToken.MatchString(token) {
			t.Fatalf("armedRecorder = %q, %q, %v; want a hex token over %s", token, got, err, dir)
		}
		if _, serr := os.Stat(filepath.Join(dir, token+".lock")); serr != nil {
			t.Fatalf("the recorder's lock file is missing: %v", serr)
		}
		again, _, err := armedRecorder()
		if err != nil || again != token {
			t.Fatalf("a second armedRecorder = %q, %v; want the same token %q", again, err, token)
		}
		recorder.mu.Lock()
		held = len(recorder.held)
		recorder.mu.Unlock()
		if held != 1 {
			t.Fatalf("the recorder holds %d locks for one directory, want 1", held)
		}
	})
}

// TestSetRecorderLockRestoresThePreviousPrimitive: the restore func puts the
// previous primitive back, and with none installed no recorder is dead.
func TestSetRecorderLockRestoresThePreviousPrimitive(t *testing.T) {
	path := filepath.Join(t.TempDir(), strings.Repeat("e", 32)+".lock")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	restore := SetRecorderLock(nil)
	if _, dead := lockDead(path); dead {
		t.Fatal("with no lock primitive an unlocked lock file was judged dead")
	}
	restore()
	release, dead := lockDead(path)
	if !dead {
		t.Fatal("after restore the previous primitive was not back: an unlocked lock file is not dead")
	}
	release()
}

// TestRecorderDeadAndLockDead: the verdicts recovery relies on, branch by
// branch. Only a missing or unreadable-as-token owner file, a missing lock,
// or a lock that can be taken proves a recorder dead; a held lock or a lock
// file that cannot be opened proves nothing.
func TestRecorderDeadAndLockDead(t *testing.T) {
	dir := t.TempDir()
	launch, token := strings.Repeat("1", 32), strings.Repeat("2", 32)
	writeOwner := func(body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, launch+".launch"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if release, dead := recorderDead(dir, launch); !dead {
		t.Fatal("a launch with no owner file was not judged dead")
	} else {
		release()
	}
	writeOwner("not a token")
	if _, dead := recorderDead(dir, launch); !dead {
		t.Fatal("an owner file naming no token was not judged dead")
	}
	writeOwner(token + "\n")
	if release, dead := recorderDead(dir, launch); !dead { // owner names a token whose lock file is gone
		t.Fatal("an owner file naming a missing lock was not judged dead")
	} else {
		release()
	}
	if _, err := os.Stat(filepath.Join(dir, launch+".launch")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("releasing a dead recorder kept its owner file: %v", err)
	}
	writeOwner(token)
	held, err := holdRecorderLock(dir, token, testFlock)
	if err != nil {
		t.Fatal(err)
	}
	if _, dead := recorderDead(dir, launch); dead {
		t.Fatal("a launch whose recorder holds its lock was judged dead")
	}
	_ = held.Close()
	release, dead := recorderDead(dir, launch)
	if !dead {
		t.Fatal("a launch whose recorder released its lock was not judged dead")
	}
	release()
	for _, name := range []string{launch + ".launch", token + ".lock"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s survived releasing a dead recorder: %v", name, err)
		}
	}
}

// TestLockDeadVerdicts: a lock path that exists but cannot be opened is not
// proof of death; a missing one is.
func TestLockDeadVerdicts(t *testing.T) {
	dir := t.TempDir()
	if _, dead := lockDead(dir); dead {
		t.Fatal("a lock path that is a directory was judged dead")
	}
	if release, dead := lockDead(filepath.Join(dir, "absent.lock")); !dead {
		t.Fatal("a missing lock file was not judged dead")
	} else {
		release()
	}
}

// TestEffectLaunchID: only a plugin.launch key with two segments and a
// plugin.call key with three, each carrying a hex launch id, names a launch.
func TestEffectLaunchID(t *testing.T) {
	id := strings.Repeat("a", 32)
	for key, want := range map[string]string{
		"plugin.launch:demo:" + id:        id,
		"plugin.call:demo:" + id + ":7":   id,
		"plugin.launch:demo":              "",
		"plugin.launch:demo:" + id + ":7": "",
		"plugin.call:demo:" + id:          "",
		"plugin.call:demo:zz:7":           "",
		"approval.grant:" + id:            "",
	} {
		got, ok := effectLaunchID(key)
		if got != want || ok != (want != "") {
			t.Fatalf("effectLaunchID(%q) = %q, %v; want %q", key, got, ok, want)
		}
	}
}

// TestRecoverPluginEffectsRefusals: a recovery that cannot reach its lock
// directory, or cannot list the pending intents, errs and arms nothing.
func TestRecoverPluginEffectsRefusals(t *testing.T) {
	f := newLaunchFix(t)
	ctx := context.Background()
	if _, err := RecoverPluginEffects(ctx, f.effects, ""); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("recovery without a lock directory = %v, want KindInvalidInput", err)
	}
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := RecoverPluginEffects(ctx, f.effects, filepath.Join(blocker, "locks")); !cascade.HasKind(err, cascade.KindUnavailable) ||
		!strings.Contains(err.Error(), "lock directory") {
		t.Fatalf("recovery under a regular file = %v, want the lock-directory refusal", err)
	}
	closeStore(t, f.store)
	n, err := RecoverPluginEffects(ctx, f.effects, f.lockDir)
	if err == nil || n != 0 {
		t.Fatalf("recovery over a closed store = %d, %v; want an error and none marked", n, err)
	}
}

// TestRecordLaunchOwnerRefusesAMissingDirectory: the owner file is how
// recovery finds a launch's recorder, so a write that fails refuses.
func TestRecordLaunchOwnerRefusesAMissingDirectory(t *testing.T) {
	dir := t.TempDir()
	id, token := strings.Repeat("3", 32), strings.Repeat("4", 32)
	if err := recordLaunchOwner(dir, id, token); err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(filepath.Join(dir, id+".launch")); err != nil || string(raw) != token {
		t.Fatalf("owner file = %q, %v; want the token", raw, err)
	}
	err := recordLaunchOwner(filepath.Join(dir, "missing"), id, token)
	if !cascade.HasKind(err, cascade.KindUnavailable) || !strings.Contains(err.Error(), "recording the launch's recorder") {
		t.Fatalf("recordLaunchOwner in a missing directory = %v, want the refusal", err)
	}
}
