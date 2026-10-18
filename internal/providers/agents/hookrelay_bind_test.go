//go:build !windows

package agents

// Purpose: the relay's lookup is bound to the spawn's project directory: no
//   walk upward, a symlinked ancestor or a directory inside another tree
//   never reaches that tree's channel, the hook payload's cwd is ignored, and
//   a hard link to another tree's callback file is refused.
// Constraints: see hookbridge_test.go (the rig). Trees are temp directories
//   on one volume, so a hard link between them works.
// SPORT: internal.providers.agents/ADD (P1-AGT-14).

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/acamarata/cascade/pkg/provider"
)

// linkedTree returns a fresh tree B holding lnk -> target.
func linkedTree(t *testing.T, target string) string {
	t.Helper()
	treeB, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(treeB, "lnk")); err != nil {
		t.Fatal(err)
	}
	return treeB
}

func TestHookRelayRefusesSymlinkedAncestor(t *testing.T) {
	t.Run("a project dir inside tree A through B's link finds nothing", func(t *testing.T) {
		a := newRig(t)
		if err := os.Mkdir(filepath.Join(a.root, "pkg"), 0o700); err != nil {
			t.Fatal(err)
		}
		treeB := linkedTree(t, a.root)
		for _, dir := range []string{treeB, filepath.Join(treeB, "lnk", "pkg"), filepath.Join(a.root, "pkg")} {
			if err := relayOnce(t, dir, HookStop, a.hook("stop.json")); err != nil {
				t.Fatalf("project dir %s: %v, want a no-op", dir, err)
			}
		}
		if a.connected() {
			t.Fatal("a project dir that is not tree A's own root reached tree A's channel")
		}
	})
	t.Run("tree B's project dir reaches only B, and a path through the link is dropped", func(t *testing.T) {
		a, b := newRig(t), newRig(t)
		if err := os.Symlink(a.root, filepath.Join(b.root, "lnk")); err != nil {
			t.Fatal(err)
		}
		b.serve(1)
		raw := bytes.ReplaceAll(b.hook("pretooluse-read.json"), []byte(b.root+"/a.txt"), []byte(b.root+"/lnk/a.txt"))
		raw = bytes.ReplaceAll(raw, []byte(`"cwd": "`+b.root), []byte(`"cwd": "`+a.root))
		if err := relayOnce(t, b.root, HookPreToolUse, raw); err != nil {
			t.Fatalf("Relay: %v", err)
		}
		b.wait()
		if frames := b.got(); len(frames) != 1 || len(frames[0].Paths) != 0 {
			t.Fatalf("tree B got %+v, want one frame with no path (a.txt resolves into tree A)", frames)
		}
		if a.connected() {
			t.Fatal("tree B's hook reached tree A's channel")
		}
	})
	t.Run("a project dir spelled through a link to tree A's own root is tree A", func(t *testing.T) {
		// The project dir comes from the harness, not the agent: a name that
		// resolves to tree A's root is tree A, so its own channel is used.
		a := newRig(t)
		a.serve(1)
		if err := relayOnce(t, filepath.Join(linkedTree(t, a.root), "lnk"), HookStop, a.hook("stop.json")); err != nil {
			t.Fatalf("Relay: %v", err)
		}
		a.wait()
		if len(a.got()) != 1 {
			t.Fatalf("tree A got %d frames, want 1", len(a.got()))
		}
	})
}

func TestHookRelayDoesNotWalkUp(t *testing.T) {
	r := newRig(t)
	sub := filepath.Join(r.root, "pkg", "inner")
	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := relayOnce(t, sub, HookStop, r.hook("stop.json")); err != nil {
		t.Fatalf("Relay: %v, want a no-op", err)
	}
	if r.connected() {
		t.Fatal("a subdirectory of the tree found the tree's callback file by walking up")
	}
}

func TestHookRelayRefusesHardLinkedCallbackFile(t *testing.T) {
	a := newRig(t)
	treeB, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(treeB, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(a.root, ".claude", provider.CallbackFileName)
	if err := os.Link(src, filepath.Join(treeB, ".claude", provider.CallbackFileName)); err != nil {
		t.Fatalf("hard-linking tree A's callback file into tree B: %v", err)
	}
	err = relayOnce(t, treeB, HookStop, a.hook("stop.json"))
	wantDenied(t, err, "the callback file is not a single-link 0600 file owned by the caller")
	if a.connected() {
		t.Fatal("a hard link to tree A's callback file sent a frame to tree A")
	}
}
