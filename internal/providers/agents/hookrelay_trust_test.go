//go:build !windows

package agents

// Purpose: the relay's trust boundaries beyond the callback file's own mode:
//   a symlinked or foreign .claude directory, a foreign token file with
//   everything else trusted, a hard-linked counter, a loose socket
//   directory, the kill-safe counter update and its exhausted maximum, and
//   tree paths spelled through a symlink.
// Constraints: see hookbridge_test.go (the rig). foreignOwner swaps the
//   package's owner lookup for one test only; no test here runs in parallel.
// SPORT: internal.providers.agents/ADD (P1-AGT-14).

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// foreignOwner makes every file named name look owned by another uid for the
// rest of the test; every other file keeps its real owner.
func foreignOwner(t *testing.T, name string) {
	t.Helper()
	orig := fileOwner
	fileOwner = func(fi os.FileInfo) (int, bool) {
		uid, ok := orig(fi)
		if fi.Name() == name {
			uid++
		}
		return uid, ok
	}
	t.Cleanup(func() { fileOwner = orig })
}

// wantDenied asserts a permission-denied refusal whose message names what.
func wantDenied(t *testing.T, err error, what string) {
	t.Helper()
	if kindOf(err) != cascade.KindPermissionDenied || !strings.Contains(err.Error(), what) {
		t.Fatalf("got %v, want a permission-denied refusal naming %q", err, what)
	}
}

func TestHookRelayRefusesSymlinkedClaudeDir(t *testing.T) {
	a := newRig(t)
	treeB, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(a.root, ".claude"), filepath.Join(treeB, ".claude")); err != nil {
		t.Fatal(err)
	}
	err = relayOnce(t, treeB, HookStop, a.hook("stop.json"))
	wantDenied(t, err, "the .claude directory is not a real directory owned by the caller")
	if a.connected() {
		t.Fatal("a relay in tree B sent a frame to tree A's driver through a symlinked .claude")
	}
}

func TestHookRelayRefusesForeignClaudeDir(t *testing.T) {
	r := newRig(t)
	foreignOwner(t, ".claude")
	wantDenied(t, relayOnce(t, r.root, HookStop, r.hook("stop.json")), "the .claude directory is not a real directory owned by the caller")
	if r.connected() {
		t.Fatal("a frame was sent from a .claude directory another user owns")
	}
}

func TestHookRelayRefusesForeignTokenFileOnly(t *testing.T) {
	r := newRig(t)
	foreignOwner(t, provider.CallbackFileName)
	err := relayOnce(t, r.root, HookStop, r.hook("stop.json"))
	wantDenied(t, err, "the callback file is not a single-link 0600 file owned by the caller")
	if r.connected() {
		t.Fatal("a frame was sent from a callback file another user owns")
	}
}

func TestHookRelayRefusesHardLinkedCounter(t *testing.T) {
	r := newRig(t)
	// The victim sits in a subdirectory of the socket's directory: a hard link
	// needs one device, and t.TempDir may live on another volume.
	other := filepath.Join(filepath.Dir(r.sock), "elsewhere")
	if err := os.Mkdir(other, 0o700); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(other, "victim")
	const secret = "SECRET-CONTENT"
	if err := os.WriteFile(victim, []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(victim, filepath.Join(filepath.Dir(r.sock), counterFileName)); err != nil {
		t.Fatalf("hard-linking the victim as the counter: %v", err)
	}
	wantDenied(t, relayOnce(t, r.root, HookStop, r.hook("stop.json")), "the sequence file is not a single-link 0600 file")
	if b, _ := os.ReadFile(victim); string(b) != secret {
		t.Fatalf("the hard-linked victim was rewritten: %q", b)
	}
	if r.connected() {
		t.Fatal("a frame was sent with a hard-linked counter")
	}
}

func TestHookRelayRefusesLooseSocketDir(t *testing.T) {
	for _, mode := range []os.FileMode{0o777, 0o750, 0o701} {
		r := newRig(t)
		dir := filepath.Dir(r.sock)
		if err := os.Chmod(dir, mode); err != nil {
			t.Fatal(err)
		}
		err := relayOnce(t, r.root, HookStop, r.hook("stop.json"))
		wantDenied(t, err, "the callback socket's directory is not a private directory owned by the caller")
		if _, serr := os.Lstat(filepath.Join(dir, counterFileName)); !errors.Is(serr, os.ErrNotExist) {
			t.Fatalf("mode %o: the counter was created in an untrusted directory (%v)", mode, serr)
		}
		if r.connected() {
			t.Fatalf("mode %o: a frame was sent through a loose socket directory", mode)
		}
	}
}

// killStore is an in-memory counter that applies only killAt more mutating
// calls, then fails every later one: the state a SIGKILL would leave.
type killStore struct {
	data   []byte
	killAt int
}

var errKilled = errors.New("killed")

func (k *killStore) ReadAt(p []byte, off int64) (int, error) {
	if off >= int64(len(k.data)) {
		return 0, io.EOF
	}
	n := copy(p, k.data[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

func (k *killStore) WriteAt(p []byte, off int64) (int, error) {
	if k.killAt--; k.killAt < 0 {
		return 0, errKilled
	}
	if end := int(off) + len(p); end > len(k.data) {
		k.data = append(k.data, make([]byte, end-len(k.data))...)
	}
	return copy(k.data[off:], p), nil
}

func (k *killStore) Truncate(n int64) error {
	if k.killAt--; k.killAt < 0 {
		return errKilled
	}
	k.data = append([]byte(nil), k.data[:min(int(n), len(k.data))]...)
	return nil
}

func TestHookRelaySeqSurvivesAKillMidUpdate(t *testing.T) {
	k := &killStore{data: []byte("aa 7\nleftover-from-an-older-record\n"), killAt: 1 << 20}
	if got, err := nextSeq(k, "aa"); err != nil || got != 8 {
		t.Fatalf("first update: %d %v, want 8", got, err)
	}
	k.killAt = 1
	if _, err := nextSeq(k, "aa"); err == nil {
		t.Fatal("the store was killed after one write, the update must report it")
	}
	k.killAt = 1 << 20
	if got, err := nextSeq(k, "aa"); err != nil || got <= 8 {
		t.Fatalf("after a kill mid-update Seq is %d (%v); it must stay above 8, never restart or repeat", got, err)
	}
}

func TestHookRelaySeqRefusesToWrap(t *testing.T) {
	stored := "aa 18446744073709551615\n"
	k := &killStore{data: []byte(stored), killAt: 1 << 20}
	_, err := nextSeq(k, "aa")
	if kindOf(err) != cascade.KindInternal || !strings.Contains(err.Error(), "the sequence counter is exhausted") {
		t.Fatalf("got %v, want a KindInternal refusal at the maximum", err)
	}
	if string(k.data) != stored {
		t.Fatalf("the refused update changed the counter: %q", k.data)
	}
}

func TestHookPathsThroughASymlinkedRoot(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	realDir, alias := filepath.Join(base, "real"), filepath.Join(base, "alias")
	if err := os.Mkdir(realDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realDir, alias); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{filepath.Join(realDir, "new.txt"), filepath.Join(alias, "new.txt")} {
		in, _ := json.Marshal(map[string]string{"file_path": file})
		if got := hookPaths(alias, in); len(got) != 1 || got[0] != "new.txt" {
			t.Fatalf("root %s, file %s: paths %v, want [new.txt]", alias, file, got)
		}
	}
	if got := hookPaths(realDir, []byte(`{"file_path":"/etc/hosts"}`)); len(got) != 0 {
		t.Fatalf("a file outside the tree leaked: %v", got)
	}
}
