// Package build (this file): the A-T2 egress import gate. It asserts the
// two allowlists in egress_allow.go member-for-member against the ruling
// text, proves the real tree carries no unlisted importer of either kind,
// and proves neither list can be used to smuggle the other kind past the
// gate.
package build

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// egressNetSpec is the network importer list the egress ruling names,
// written out here so the assertion has a side to compare against that is
// not the implementation's own copy.
var egressNetSpec = []string{
	"internal/secrets",
	"internal/providers/intake",
	"providers/embeddings/bgem3",
	"providers/openai",
	"providers/redis",
	"providers/s3",
	"providers/transport",
	"internal/plugins/remote",
	"internal/plugins/registryfetch",
	"internal/nodes",
	"plugins/cascade-pa/telegram",
	"plugins/github",
	"plugins/github/tools",
	"plugins/pbd",
	"internal/providers/agents/egressproxy",
	"internal/update",
}

// egressExecSpec is the process-spawn list the ruling names. It has one
// member, and it is not a member of the network list.
var egressExecSpec = []string{"internal/plugins/process"}

// TestArchEgressImportAllowlist asserts the normative network list
// member-for-member and proves the real tree adds no unlisted importer.
func TestArchEgressImportAllowlist(t *testing.T) {
	assertMembers(t, "net", egressAllowDirs(EgressNetNormative), egressNetSpec)
	assertReasonsPresent(t, EgressNetNormative)
	assertReasonsPresent(t, EgressNetNotYetMigrated)
	assertReasonsPresent(t, EgressNetLocalTransport)
	assertMembers(t, "local", egressAllowDirs(EgressNetLocalTransport), []string{
		"internal/client", "internal/daemon", "internal/rpc", "internal/fleet/sessions",
	})
	for _, dir := range egressAllowDirs(EgressNetLocalTransport) {
		if EgressDirAllowed(dir, egressAllowDirs(EgressNetNormative, EgressNetNotYetMigrated)) {
			t.Errorf("local transport %s is duplicated in an egress list", dir)
		}
	}

	allowed := egressAllowDirs(EgressNetNormative, EgressNetNotYetMigrated, EgressNetLocalTransport)
	violations, err := ScanAgentDriverNetSurface(archModuleRoot(t))
	if err != nil || len(violations) != 0 {
		t.Fatalf("agent driver surface: %v, %v", violations, err)
	}
	for _, importer := range scanRealTree(t, EgressNetImports) {
		if strings.HasPrefix(importer.Dir, "providers/agents/") {
			continue
		}
		if !EgressDirAllowed(importer.Dir, allowed) {
			t.Errorf("%s imports %q but is on neither network list; route it through the egress firewall or list it with a reason",
				importer.Dir, importer.Import)
		}
	}
}

func TestArchEgressEveryEntryHasPurpose(t *testing.T) {
	path := filepath.Join(archModuleRoot(t), "internal", "hooks", "egress", "classes.go")
	f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	valid := map[string]bool{PurposeLocalTransport: true, PurposeTypeOnly: true, PurposeProcessTierDeclared: true}
	classes := 0
	for _, d := range f.Decls {
		g, ok := d.(*ast.GenDecl)
		if !ok || g.Tok != token.CONST {
			continue
		}
		for _, s := range g.Specs {
			v := s.(*ast.ValueSpec)
			id, ok := v.Type.(*ast.Ident)
			if !ok || id.Name != "EgressClass" {
				continue
			}
			for _, e := range v.Values {
				lit, ok := e.(*ast.BasicLit)
				if !ok {
					t.Fatal("nonliteral egress class")
				}
				value, err := strconv.Unquote(lit.Value)
				if err != nil {
					t.Fatal(err)
				}
				valid[value] = true
				classes++
			}
		}
	}
	if classes == 0 {
		t.Fatal("no class declarations parsed")
	}
	dirs := egressAllowDirs(EgressNetNormative, EgressNetNotYetMigrated, EgressNetLocalTransport)
	if len(dirs) == 0 {
		t.Fatal("no network entries")
	}
	for _, dir := range dirs {
		if !valid[EgressNetPurpose[dir]] {
			t.Errorf("%s has invalid purpose %q", dir, EgressNetPurpose[dir])
		}
	}
}

// TestArchOsExecAllowlist asserts the process-spawn list and the two
// cross-list smuggling routes the gate must refuse.
func TestArchOsExecAllowlist(t *testing.T) {
	assertMembers(t, "os/exec", egressAllowDirs(EgressExecNormative), egressExecSpec)
	assertReasonsPresent(t, EgressExecNormative)
	assertReasonsPresent(t, EgressExecNotYetMigrated)

	allowed := egressAllowDirs(EgressExecNormative, EgressExecNotYetMigrated)
	for _, importer := range scanRealTree(t, EgressExecImports) {
		if !EgressDirAllowed(importer.Dir, allowed) {
			t.Errorf("%s imports %q but is on neither process-spawn list", importer.Dir, importer.Import)
		}
	}

	// Smuggle 1: a network importer must not be admitted by the process
	// list. internal/plugins/process is the sole member of that list, and
	// it must not appear on the network list.
	netAllowed := egressAllowDirs(EgressNetNormative, EgressNetNotYetMigrated)
	if EgressDirAllowed("internal/plugins/process", netAllowed) {
		t.Error("internal/plugins/process appears on the network list; process spawn is not egress")
	}
	// Smuggle 2: the network list's members must not be admitted by the
	// process list.
	execAllowed := egressAllowDirs(EgressExecNormative)
	for _, dir := range egressNetSpec {
		if EgressDirAllowed(strings.TrimSuffix(dir, "/*"), execAllowed) {
			t.Errorf("%s is admitted by the process-spawn list; the lists must not merge", dir)
		}
	}
}

// TestArchEgressGateCatchesAnUnlistedImporter proves the gate can fail:
// a fabricated importer outside every pattern is refused.
func TestArchEgressGateCatchesAnUnlistedImporter(t *testing.T) {
	allowed := egressAllowDirs(EgressNetNormative, EgressNetNotYetMigrated)
	if EgressDirAllowed("internal/brand/new/leaker", allowed) {
		t.Fatal("an unlisted package was admitted by the network list")
	}
	if EgressDirAllowed("internal/secretsx", allowed) {
		t.Fatal("a prefix-adjacent package was admitted; patterns must match whole segments")
	}
	if !EgressDirAllowed("providers/embeddings/bgem3", allowed) {
		t.Fatal("the exact embedding provider entry was not admitted")
	}
	if EgressDirAllowed("providers", allowed) {
		t.Fatal("an unlisted parent directory was admitted")
	}
}

// scanRealTree scans every root the gate governs.
func scanRealTree(t *testing.T, imports []string) []EgressImporter {
	t.Helper()
	out, err := scanEgressTree(archModuleRoot(t), imports)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func scanEgressTree(root string, imports []string) ([]EgressImporter, error) {
	var out []EgressImporter
	for _, tree := range []string{"cmd", "internal", "pkg", "providers", "plugins"} {
		dir := filepath.Join(root, tree)
		found, err := ScanEgressImporters(dir, imports)
		if err != nil {
			return nil, fmt.Errorf("scan %s: %w", tree, err)
		}
		for _, importer := range found {
			rel := tree
			if importer.Dir != "" {
				rel = tree + "/" + importer.Dir
			}
			out = append(out, EgressImporter{Dir: rel, Import: importer.Import})
		}
	}
	return out, nil
}

func TestArchEgressNoWildcards(t *testing.T) {
	for _, dir := range egressAllowDirs(EgressNetNormative, EgressNetNotYetMigrated,
		EgressNetLocalTransport, EgressExecNormative, EgressExecNotYetMigrated) {
		if strings.HasSuffix(dir, "/*") {
			t.Errorf("wildcard %q", dir)
		}
	}
	for _, tc := range []struct {
		dir, pattern string
		want         bool
	}{
		{"providers/openai", "providers/*", false}, {"providers", "providers/*", false},
		{"providers/openai", "providers/openai", true}, {"providers/openai/x", "providers/openai", false},
	} {
		if got := egressPatternMatches(tc.dir, tc.pattern); got != tc.want {
			t.Errorf("match(%q, %q)=%v, want %v", tc.dir, tc.pattern, got, tc.want)
		}
	}
}

func TestArchEgressEntriesAreLive(t *testing.T) {
	live := map[string]bool{}
	for _, imp := range scanRealTree(t, EgressNetImports) {
		live[imp.Dir] = true
	}
	if len(live) == 0 {
		t.Fatal("empty importer inventory")
	}
	for _, dir := range egressAllowDirs(EgressNetNormative, EgressNetNotYetMigrated, EgressNetLocalTransport) {
		if !live[dir] {
			t.Errorf("stale network entry %s", dir)
		}
	}
}

func TestArchEgressScanFailsClosed(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"cmd", "internal", "pkg", "providers", "plugins"} {
		if err := os.Mkdir(filepath.Join(root, dir), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(root, "internal", "broken.go")
	for _, body := range []string{"package", "package broken\nfunc unfinished("} {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := ScanEgressImporters(root, EgressNetImports); err == nil {
			t.Error("scanner accepted a parse error")
		}
		if _, err := scanEgressTree(root, EgressNetImports); err == nil {
			t.Error("gate helper accepted a parse error")
		}
	}
}

// assertMembers compares two directory lists member-for-member.
func assertMembers(t *testing.T, kind string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s allowlist has %d members, the ruling names %d:\ngot  %v\nwant %v", kind, len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%s allowlist member %d = %q, the ruling names %q", kind, i, got[i], want[i])
		}
	}
}

// assertReasonsPresent proves no entry was added without stating why.
func assertReasonsPresent(t *testing.T, entries []EgressAllowEntry) {
	t.Helper()
	for _, entry := range entries {
		if strings.TrimSpace(entry.Reason) == "" {
			t.Errorf("allowlist entry %q carries no reason", entry.Dir)
		}
		if strings.TrimSpace(entry.Dir) == "" {
			t.Error("an allowlist entry has an empty directory")
		}
	}
}
