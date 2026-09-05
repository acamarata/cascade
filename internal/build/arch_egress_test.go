// Package build (this file): the A-T2 egress import gate. It asserts the
// two allowlists in egress_allow.go member-for-member against the ruling
// text, proves the real tree carries no unlisted importer of either kind,
// and proves neither list can be used to smuggle the other kind past the
// gate.
package build

import (
	"path/filepath"
	"strings"
	"testing"
)

// egressNetSpec is the network importer list the egress ruling names,
// written out here so the assertion has a side to compare against that is
// not the implementation's own copy.
var egressNetSpec = []string{
	"internal/secrets",
	"internal/providers/intake",
	"internal/providers/health",
	"providers/*",
	"internal/backup/targets",
	"internal/plugins/remote",
	"internal/plugins/registryfetch",
	"internal/nodes",
	"internal/sync",
	"plugins/*",
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

	allowed := egressAllowDirs(EgressNetNormative, EgressNetNotYetMigrated)
	for _, importer := range scanRealTree(t, EgressNetImports) {
		if !EgressDirAllowed(importer.Dir, allowed) {
			t.Errorf("%s imports %q but is on neither network list; route it through the egress firewall or list it with a reason",
				importer.Dir, importer.Import)
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
		t.Fatal("the providers/* pattern did not admit a package beneath it")
	}
	if !EgressDirAllowed("providers", allowed) {
		t.Fatal("the providers/* pattern did not admit its own base directory")
	}
}

// scanRealTree scans every root the gate governs.
func scanRealTree(t *testing.T, imports []string) []EgressImporter {
	t.Helper()
	root := archModuleRoot(t)
	var out []EgressImporter
	for _, tree := range []string{"cmd", "internal", "pkg", "providers", "plugins"} {
		dir := filepath.Join(root, tree)
		found, err := ScanEgressImporters(dir, imports)
		if err != nil {
			continue
		}
		for _, importer := range found {
			rel := tree
			if importer.Dir != "" {
				rel = tree + "/" + importer.Dir
			}
			out = append(out, EgressImporter{Dir: rel, Import: importer.Import})
		}
	}
	return out
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
