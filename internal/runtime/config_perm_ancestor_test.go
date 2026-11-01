//go:build !windows

package runtime

import (
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// ownerCaseEntries snapshots only the test's temporary tree for owner fakes.
func ownerCaseEntries(t *testing.T, path string) []fs.FileInfo {
	t.Helper()
	root := filepath.Dir(path)
	for !strings.HasPrefix(filepath.Base(root), "Test") {
		if filepath.Dir(root) == root {
			t.Fatal("config outside test temporary tree")
		}
		root = filepath.Dir(root)
	}
	var entries []fs.FileInfo
	err := filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		fi, err := d.Info()
		if err == nil {
			entries = append(entries, fi)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

// TestConfigLinkChainBound proves a symlink chain longer than
// maxConfigLinkHops is refused, while one at the bound is walked.
func TestConfigLinkChainBound(t *testing.T) {
	for _, c := range []struct {
		hops   int
		refuse bool
	}{{maxConfigLinkHops, false}, {maxConfigLinkHops + 1, true}} {
		isolateHome(t)
		dir := filepath.Dir(mkConfig(t, 0o700, 0o600))
		prev := "config.toml"
		for i := 0; i < c.hops; i++ {
			name := "hop" + strconv.Itoa(i)
			if err := os.Symlink(prev, filepath.Join(dir, name)); err != nil {
				t.Fatal(err)
			}
			prev = name
		}
		got, err := CheckConfigPermissions(filepath.Join(dir, prev))
		if err != nil || (got.Level == ConfigPermRefuse) != c.refuse || (c.refuse && !strings.Contains(got.Reason, "symlinks")) {
			t.Fatalf("%d hops: %+v, %v; want refuse=%v", c.hops, got, err, c.refuse)
		}
	}
}

// ancestorCase is one TestConfigAncestorWalk layout: loose (mode) holds
// cfgdir, a real directory or a link to a safe sibling (link), and the
// config sits in cfgdir or, with parent, in loose itself.
type ancestorCase struct {
	name                 string
	mode                 os.FileMode
	link, parent, refuse bool
}

// TestConfigAncestorWalk checks resolved ancestors and directory link holders.
func TestConfigAncestorWalk(t *testing.T) {
	for _, c := range []ancestorCase{
		{"loose directory link", 0o777, true, false, true},
		{"safe directory link", 0o700, true, false, false},
		{"group writable grandparent", 0o770, false, false, true},
		{"world writable grandparent", 0o777, false, false, true},
		{"relative world writable grandparent", 0o777, false, false, true},
		{"safe grandparent", 0o750, false, false, false},
		{"user sticky parent", 0o777 | os.ModeSticky, false, true, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			if c.parent && os.Geteuid() == 0 {
				t.Skip("root owns the sticky directory")
			}
			isolateHome(t)
			path, loose := mkAncestorCase(t, c)
			assertAncestorVerdict(t, path, loose, c.refuse)
		})
	}
}

// mkAncestorCase builds c's layout under t.TempDir() and returns the config
// path (relative to a new working directory for a "relative " case) and the
// loose directory.
func mkAncestorCase(t *testing.T, c ancestorCase) (path, loose string) {
	t.Helper()
	base := t.TempDir()
	loose = filepath.Join(base, "loose")
	if err := os.Mkdir(loose, 0o700); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(loose, "cfgdir")
	if c.link {
		safe := filepath.Join(base, "safe")
		if err := os.Mkdir(safe, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(safe, dir); err != nil {
			t.Fatal(err)
		}
	} else if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if c.parent {
		dir = loose
	}
	path = filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("schema_version = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(loose, c.mode); err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(c.name, "relative ") {
		t.Chdir(base)
		path = filepath.Join("loose", "cfgdir", "config.toml")
	}
	return path, loose
}

func assertAncestorVerdict(t *testing.T, path, dir string, refuse bool) {
	t.Helper()
	finding, err := CheckConfigPermissions(path)
	if err != nil {
		t.Fatal(err)
	}
	loadErr := loadWithEnv(t, path, nil, &warnCollector{})
	if !refuse {
		if finding.Level != ConfigPermOK || loadErr != nil {
			t.Fatalf("check=%+v, Load=%v", finding, loadErr)
		}
		return
	}
	if finding.Level != ConfigPermRefuse || !strings.Contains(finding.Reason, dir) {
		t.Fatalf("check=%+v; want refusal naming %s", finding, dir)
	}
	if kind, ok := cascade.KindOf(loadErr); !ok || kind != cascade.KindPermissionDenied {
		t.Fatalf("Load=%v; want KindPermissionDenied", loadErr)
	}
}

// TestConfigAncestorRootStickyViaSeam requires root ownership for sticky bypass.
func TestConfigAncestorRootStickyViaSeam(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("user-owned sticky negative control needs a non-root uid")
	}
	for _, c := range []struct {
		name   string
		mode   os.FileMode
		uid    int
		refuse bool
	}{
		{"root sticky", 0o777 | os.ModeSticky, 0, false},
		{"user sticky", 0o777 | os.ModeSticky, os.Geteuid(), true},
		{"root nonsticky", 0o777, 0, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			isolateHome(t)
			path := mkConfig(t, c.mode, 0o600)
			dir := filepath.Dir(path)
			created, err := os.Stat(dir)
			if err != nil {
				t.Fatal(err)
			}
			setOwnerUIDLookup(t, func(fi fs.FileInfo) (int, bool) {
				if os.SameFile(created, fi) {
					return c.uid, true
				}
				return ownerUID(fi)
			})
			assertAncestorVerdict(t, path, dir, c.refuse)
		})
	}
}

// TestConfigWalkDotDotAfterLink proves a ".." after a directory link
// climbs from the link's target, as the kernel does, not from the link's
// spelling: lexical cleaning would end the walk away from the opened file.
func TestConfigWalkDotDotAfterLink(t *testing.T) {
	isolateHome(t)
	base := t.TempDir()
	inner := filepath.Join(base, "safe", "inner")
	if err := os.MkdirAll(inner, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "safe", "config.toml"), []byte("schema_version = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(base, "spelled"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(inner, filepath.Join(base, "spelled", "cfglink")); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(base, "spelled", "cfglink") + "/../config.toml"
	assertAncestorVerdict(t, path, "", false)
}

// TestConfigDirLinkBound proves directory-component links have their own
// bound, maxConfigDirLinkHops, reported with that number. The walk is
// called directly: filepath.EvalSymlinks, which runs first in
// openConfigChecked, stops at the same total.
func TestConfigDirLinkBound(t *testing.T) {
	for _, c := range []struct {
		hops   int
		refuse bool
	}{{maxConfigDirLinkHops, false}, {maxConfigDirLinkHops + 1, true}} {
		// Resolve the temporary root first, so no system link (/tmp ->
		// /private/tmp) in the prefix adds to the count; every hop is relative.
		resolved, err := filepath.EvalSymlinks(mkConfig(t, 0o700, 0o600))
		if err != nil {
			t.Fatal(err)
		}
		root, prev := filepath.Dir(filepath.Dir(resolved)), "home"
		for i := 0; i < c.hops; i++ {
			name := "d" + strconv.Itoa(i)
			if err := os.Symlink(prev, filepath.Join(root, name)); err != nil {
				t.Fatal(err)
			}
			prev = name
		}
		path := filepath.Join(root, prev, "config.toml")
		_, got, err := linkHopDirs(configTarget{path: path, real: resolved})
		want := strconv.Itoa(maxConfigDirLinkHops) + " directory symlinks"
		if err != nil || (got.Level == ConfigPermRefuse) != c.refuse || (c.refuse && !strings.Contains(got.Reason, want)) {
			t.Fatalf("%d directory links: %+v, %v; want refuse=%v naming %q", c.hops, got, err, c.refuse, want)
		}
	}
}
