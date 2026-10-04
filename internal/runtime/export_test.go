//go:build !windows

package runtime

// Purpose: the test-only owner seam for the unix config permission
//   classifier. ownerUIDLookup (config_perm_unix.go) is set only from this
//   file, so the foreign-owner refusals run as a normal user in CI instead
//   of only as root in the container run; a gate proves no production file
//   can set it. Also the unix-only checked-descriptor, name-recheck and
//   link-chain-bound tests.
// Inputs: real files under t.TempDir(); a fake owner lookup per test.
// Outputs: n/a (test-only).
// Constraints: unix only (the seam lives in config_perm_unix.go), so the
//   windows test compile of this package does not see it. Tests that swap
//   the seam are not parallel and restore it in t.Cleanup.
// SPORT: runtime/config (ADD, P1-CORE-16).

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

const ownerSeamName = "ownerUIDLookup"

// ownerSeamRefs parses every non-test .go file in dir and returns how many
// times the seam is declared and called, plus every other reference
// (assignment, address-of, use as a value), by position.
func ownerSeamRefs(t *testing.T, dir string) (decls, calls int, bad []string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(dir, e.Name()), nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", e.Name(), err)
		}
		allowed := map[*ast.Ident]bool{}
		ast.Inspect(file, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.ValueSpec:
				for _, id := range x.Names {
					if id.Name == ownerSeamName {
						allowed[id] = true
						decls++
					}
				}
			case *ast.CallExpr:
				if id, ok := x.Fun.(*ast.Ident); ok && id.Name == ownerSeamName {
					allowed[id] = true
					calls++
				}
			}
			return true
		})
		ast.Inspect(file, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok && id.Name == ownerSeamName && !allowed[id] {
				bad = append(bad, fset.Position(id.Pos()).String())
			}
			return true
		})
	}
	return decls, calls, bad
}

// linknamesTo returns every //go:linkname directive naming sym in any .go
// file under root.
func linknamesTo(t *testing.T, root, sym string) []string {
	t.Helper()
	var hits []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && path != root && (strings.HasPrefix(d.Name(), ".") || d.Name() == "vendor" || d.Name() == "node_modules") {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(data), "\n") {
			if l := strings.TrimSpace(line); strings.HasPrefix(l, "//go:linkname") && strings.Contains(l, sym) {
				hits = append(hits, path+":"+strconv.Itoa(i+1))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return hits
}

// TestOwnerSeamHasNoProductionSetter proves the owner seam is declared once
// and only called in production code: nothing outside _test.go files can
// assign it, take its address, pass it as a value or linkname it.
func TestOwnerSeamHasNoProductionSetter(t *testing.T) {
	decls, calls, bad := ownerSeamRefs(t, ".")
	if decls != 1 || calls == 0 {
		t.Fatalf("%s: %d declarations and %d calls in production files, want exactly 1 and at least 1", ownerSeamName, decls, calls)
	}
	for _, b := range bad {
		t.Errorf("%s referenced outside its declaration and calls at %s", ownerSeamName, b)
	}
	for _, h := range linknamesTo(t, filepath.Join("..", ".."), ownerSeamName) {
		t.Errorf("//go:linkname names %s at %s", ownerSeamName, h)
	}
}

// setOwnerUIDLookup swaps the owner seam for one test and restores it.
func setOwnerUIDLookup(t *testing.T, fn func(fs.FileInfo) (int, bool)) {
	t.Helper()
	prev := ownerUIDLookup
	ownerUIDLookup = fn
	t.Cleanup(func() { ownerUIDLookup = prev })
}

// ownerCase is the uid the seam reports for the file, each directory and the symlink named foreignLink.
type ownerCase struct {
	name                     string
	fileUID, dirUID, linkUID int
	foreignLink              string                    // symlink base name reported as linkUID
	setup                    func(t *testing.T) string // returns the config path
	substr                   string                    // non-empty: refused, reason carries it
}

// ownerSeamCases: in a sticky directory only a link's owner can replace it.
func ownerSeamCases() []ownerCase {
	me, foreign := os.Geteuid(), os.Geteuid()+4242
	owned := "owned by uid " + strconv.Itoa(foreign)
	safe := func(t *testing.T) string { return mkConfig(t, 0o700, 0o600) }
	stickyLink := func(t *testing.T) string { return mkSymlinkConfig(t, 0o777|os.ModeSticky, safe(t)) }
	twoHop := func(t *testing.T) string {
		mid := filepath.Join(filepath.Dir(stickyLink(t)), "mid.toml")
		if err := os.Rename(filepath.Join(filepath.Dir(mid), "config.toml"), mid); err != nil {
			t.Fatal(err)
		}
		return mkSymlinkConfig(t, 0o700, mid)
	}
	return []ownerCase{
		{name: "foreign-owned file refused", fileUID: foreign, dirUID: me, setup: safe, substr: owned},
		{name: "foreign-owned parent directory refused", fileUID: me, dirUID: foreign, setup: safe, substr: owned},
		{name: "owned by the running user accepted", fileUID: me, dirUID: me, setup: safe},
		{name: "owned by root accepted", setup: safe},
		{name: "foreign-owned link in a sticky 1777 dir to a 0600 file refused", fileUID: me, dirUID: me, linkUID: foreign,
			foreignLink: "config.toml", setup: stickyLink, substr: "symlink %s " + owned},
		{name: "link in a sticky 1777 dir owned by the running user accepted", fileUID: me, dirUID: me, linkUID: me,
			foreignLink: "config.toml", setup: stickyLink},
		{name: "two-hop chain whose middle link is foreign-owned refused", fileUID: me, dirUID: me, linkUID: foreign,
			foreignLink: "mid.toml", setup: twoHop, substr: "mid.toml " + owned},
	}
}

// TestForeignOwnerRefusedViaSeam runs the foreign-owner refusals as any
// user: the seam reports a uid that is neither root nor the running user
// for the file, its parent directory, or a symlink on the config path.
func TestForeignOwnerRefusedViaSeam(t *testing.T) {
	for _, c := range ownerSeamCases() {
		t.Run(c.name, func(t *testing.T) {
			isolateHome(t)
			path, links := runOwnerSeamSetup(t, c)
			got, err := CheckConfigPermissions(path)
			if err != nil {
				t.Fatalf("CheckConfigPermissions: %v", err)
			}
			_, loadErr := Load(context.Background(), LoadOptions{Path: path,
				Getenv: func(string) string { return "" }, Environ: func() []string { return nil }})
			if c.substr == "" {
				if got.Level != ConfigPermOK || loadErr != nil {
					t.Fatalf("level %q (%s), Load err %v; want ok and a clean Load", got.Level, got.Reason, loadErr)
				}
			} else if want := strings.ReplaceAll(c.substr, "%s", path); got.Level != ConfigPermRefuse || !strings.Contains(got.Reason, want) {
				t.Fatalf("level %q reason %q, want refuse naming %q", got.Level, got.Reason, want)
			} else if kind, ok := cascade.KindOf(loadErr); !ok || kind != cascade.KindPermissionDenied || !strings.Contains(loadErr.Error(), path) {
				t.Fatalf("Load err = %v, want KindPermissionDenied naming %s", loadErr, path)
			}
			// The link's owner was consulted, not skipped: once per classification.
			if c.foreignLink != "" && *links < 2 {
				t.Fatalf("owner seam saw link %s %d times, want once per classification (2)", c.foreignLink, *links)
			}
		})
	}
}

// runOwnerSeamSetup builds the files, installs the seam and counts its calls on c.foreignLink.
func runOwnerSeamSetup(t *testing.T, c ownerCase) (string, *int) {
	t.Helper()
	path, links := c.setup(t), new(int)
	setOwnerUIDLookup(t, func(fi fs.FileInfo) (int, bool) {
		if fi.Mode()&os.ModeSymlink != 0 && fi.Name() == c.foreignLink {
			*links++
			return c.linkUID, true
		}
		return map[bool]int{true: c.dirUID, false: c.fileUID}[fi.IsDir()], true
	})
	return path, links
}

// TestConfigReadUsesCheckedDescriptor proves Load reads the descriptor it
// classified: the group-writable warning fires between the check and the
// read, and the file swapped in from that callback is not what Load parses.
func TestConfigReadUsesCheckedDescriptor(t *testing.T) {
	skipUnlessUnix(t)
	isolateHome(t)
	path := mkConfig(t, 0o700, 0o660)
	swapped := false
	swap := func(string, ...interface{}) {
		tmp := path + ".swap"
		if err := os.WriteFile(tmp, []byte("not = = toml\n"), 0o600); err != nil || os.Rename(tmp, path) != nil {
			t.Errorf("swap failed: %v", err)
		}
		swapped = true
	}
	_, err := Load(context.Background(), LoadOptions{Path: path, Getenv: func(string) string { return "" },
		Environ: func() []string { return nil }, Warn: swap})
	if !swapped || err != nil {
		t.Fatalf("swapped=%v, Load err = %v; want the checked file's bytes, not the swapped file", swapped, err)
	}
}

// TestCheckRefusesNameSwappedAfterOpen proves the resolved name is
// rechecked against the opened file before its directory is judged: the
// owner seam, called for the opened file, renames a different file over
// the name, and both the check and Load refuse instead of vouching for a
// directory that no longer holds the bytes read.
func TestCheckRefusesNameSwappedAfterOpen(t *testing.T) {
	isolateHome(t)
	path := mkConfig(t, 0o700, 0o600)
	swaps := 0
	setOwnerUIDLookup(t, func(fi fs.FileInfo) (int, bool) {
		if !fi.IsDir() {
			swaps++
			tmp := path + ".swap"
			if err := os.WriteFile(tmp, []byte("schema_version = 1\n"), 0o600); err != nil || os.Rename(tmp, path) != nil {
				t.Errorf("swap failed: %v", err)
			}
		}
		return os.Geteuid(), true
	})
	const want = "changed while it was being checked"
	got, err := CheckConfigPermissions(path)
	if err != nil || got.Level != ConfigPermRefuse || !strings.Contains(got.Reason, want) {
		t.Fatalf("CheckConfigPermissions = %+v, %v; want refuse naming %q", got, err, want)
	}
	_, loadErr := Load(context.Background(), LoadOptions{Path: path,
		Getenv: func(string) string { return "" }, Environ: func() []string { return nil }})
	if kind, ok := cascade.KindOf(loadErr); !ok || kind != cascade.KindPermissionDenied || !strings.Contains(loadErr.Error(), want) {
		t.Fatalf("Load err = %v, want KindPermissionDenied naming %q", loadErr, want)
	}
	if swaps != 2 {
		t.Fatalf("owner seam swapped the file %d times, want once per classification (2)", swaps)
	}
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
