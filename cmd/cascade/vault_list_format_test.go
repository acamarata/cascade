// Purpose: DEFECT-vault-list-raw-struct-output.md's regression test.
//
//	Before listView (vault.go) grew a String() method, output.Writer.Result
//	fell back to fmt's default `%v` verb and printed the raw struct
//	literal, e.g. "{[ALPHA] file-vault}" on a populated vault or
//	"{[] file-vault}" on an empty one. vault_test.go's
//	TestVaultCLIListEmitsNamesOnly does not catch this: its
//	strings.Contains(name) check still passes, because a stored name's
//	substring still appears inside the bracketed slice literal — this is
//	the too-weak assertion the mutation proof below demonstrates.
//
// Inputs/Outputs: drives the real `cascade vault list` CLI entry point
//
//	(runVault, vault_test.go) over an empty and a one-entry file vault.
//
// Constraints: split into its own file (not added to vault_test.go) to
//
//	keep that file under the 300-line cap (Art.10.3).
//
// SPORT: cmd/cascade/vault (DEFECT fix, no sport_updates — no new
//
//	exported surface, only listView.String()).
package main

import (
	"strings"
	"testing"
)

// TestVaultCLIListHumanModeIsFormattedNotRawStruct asserts the human-mode
// shape directly: no bare "{[" struct-literal marker, and the documented
// "backend: ..." / "names: N" text instead, on both an empty and a
// populated vault.
func TestVaultCLIListHumanModeIsFormattedNotRawStruct(t *testing.T) {
	deps := testVaultDeps(t, okGate{}, nil)

	stdout, _, err := runVault(t, deps, "", "list")
	if err != nil {
		t.Fatalf("list (empty vault): %v", err)
	}
	assertListIsFormatted(t, stdout, "names: 0")

	if _, _, err := runVault(t, deps, "s3cr3t\n", "set", "API_TOKEN"); err != nil {
		t.Fatalf("set: %v", err)
	}
	stdout, _, err = runVault(t, deps, "", "list")
	if err != nil {
		t.Fatalf("list (populated vault): %v", err)
	}
	assertListIsFormatted(t, stdout, "names: 1", "  API_TOKEN")
}

// assertListIsFormatted fails the test if stdout looks like fmt's default
// struct-literal rendering, or does not contain each wanted substring.
func assertListIsFormatted(t *testing.T, stdout string, want ...string) {
	t.Helper()
	if strings.Contains(stdout, "{[") {
		t.Fatalf("list stdout %q still looks like a raw Go struct literal", stdout)
	}
	if !strings.HasPrefix(stdout, "backend: ") {
		t.Fatalf("list stdout %q does not start with the documented 'backend: ' line", stdout)
	}
	for _, w := range want {
		if !strings.Contains(stdout, w) {
			t.Fatalf("list stdout %q is missing %q", stdout, w)
		}
	}
}
