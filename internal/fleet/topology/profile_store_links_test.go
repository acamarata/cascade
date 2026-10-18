package topology

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// danglingCase seeds a persistent claude-cli profile per id in a fresh store
// and isolated root.
func danglingCase(t *testing.T, ids ...RuntimeProfileID) (*Store, string) {
	t.Helper()
	root := isolatedRoot(t)
	s := newTestStore(t)
	for _, id := range ids {
		seedEndpointProfile(t, s, id, RuntimeClaudeCLI, true)
	}
	return s, root
}

// absentSibling is the path of acct-b's home, which no test creates.
func absentSibling(root string) string {
	p, _ := HomePath(root, "acct-b")
	return p
}

// setRuntimeConfigDanglingLinks: a link that does not resolve yet is not a
// directory that has not been created yet. A dangling link inside the home
// and one AT the home are refused, so a later mkdir of the target cannot
// move the config elsewhere.
func setRuntimeConfigDanglingLinks(t *testing.T) {
	t.Helper()
	skipOnWindows(t)
	ctx := context.Background()
	t.Run("link inside home to absent sibling home", func(t *testing.T) {
		s, root := danglingCase(t, "acct-a")
		home := makeHome(t, root, "acct-a")
		if err := os.Symlink(absentSibling(root), filepath.Join(home, "dl")); err != nil {
			t.Fatal(err)
		}
		requireSentinel(t, s.SetRuntimeConfig(ctx, "acct-a", "X_HOME", filepath.Join(home, "dl", "cfg"), root), ErrTopologyInvariant, "acct-a")
	})
	t.Run("home is a dangling link to absent sibling home", func(t *testing.T) {
		s, root := danglingCase(t, "acct-a")
		home, _ := HomePath(root, "acct-a")
		if err := os.MkdirAll(filepath.Dir(home), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(absentSibling(root), home); err != nil {
			t.Fatal(err)
		}
		requireSentinel(t, s.SetRuntimeConfig(ctx, "acct-a", "X_HOME", filepath.Join(home, "cfg"), root), ErrTopologyInvariant, "acct-a")
	})
}

// setRuntimeConfigHomeKinds: a file AT the home and a symlinked profiles/
// are refused too, even when the config home is the home itself.
func setRuntimeConfigHomeKinds(t *testing.T) {
	t.Helper()
	skipOnWindows(t)
	ctx := context.Background()
	t.Run("home is a file and is the config home", func(t *testing.T) {
		s, root := danglingCase(t, "acct-f")
		home, _ := HomePath(root, "acct-f")
		if err := os.MkdirAll(filepath.Dir(home), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(home, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		requireSentinel(t, s.SetRuntimeConfig(ctx, "acct-f", "X_HOME", home, root), ErrTopologyInvariant, "acct-f")
	})
	t.Run("profiles dir is a link", func(t *testing.T) {
		s, root := danglingCase(t, "acct-a")
		if err := os.Symlink(t.TempDir(), filepath.Join(root, profilesDirName)); err != nil {
			t.Fatal(err)
		}
		home, _ := HomePath(root, "acct-a")
		requireSentinel(t, s.SetRuntimeConfig(ctx, "acct-a", "X_HOME", filepath.Join(home, "cfg"), root), ErrTopologyInvariant, "acct-a")
	})
}

// setRuntimeConfigHomeLinks: a symlink or a file AT the home path never
// counts as the home, so one account cannot borrow a sibling's home.
func setRuntimeConfigHomeLinks(t *testing.T) {
	t.Helper()
	skipOnWindows(t)
	ctx := context.Background()
	root := isolatedRoot(t)
	s := newTestStore(t)
	for _, id := range []RuntimeProfileID{"acct-a", "acct-f"} {
		seedEndpointProfile(t, s, id, RuntimeClaudeCLI, true)
	}
	sibling := makeHome(t, root, "acct-b")
	linkHome, _ := HomePath(root, "acct-a")
	if err := os.Symlink(sibling, linkHome); err != nil {
		t.Fatal(err)
	}
	fileHome, _ := HomePath(root, "acct-f")
	if err := os.WriteFile(fileHome, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, _ := s.GetRuntimeProfile(ctx, "acct-a")
	rows := []struct {
		id  RuntimeProfileID
		cfg string
	}{
		{"acct-a", filepath.Join(sibling, "cfg")},
		{"acct-a", filepath.Join(linkHome, "cfg")},
		{"acct-a", linkHome},
		{"acct-f", filepath.Join(fileHome, "cfg")},
	}
	for _, r := range rows {
		requireSentinel(t, s.SetRuntimeConfig(ctx, r.id, "X_HOME", r.cfg, root), ErrTopologyInvariant, string(r.id))
	}
	if now, _ := s.GetRuntimeProfile(ctx, "acct-a"); now != before {
		t.Errorf("row changed %+v -> %+v", before, now)
	}
}
