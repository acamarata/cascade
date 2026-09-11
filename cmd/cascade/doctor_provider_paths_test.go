package main

// Purpose: pins the one property whose absence let `cascade doctor` report
// on a provider store the operator never configured.
//
// providerHealthSourceFor used to take a runtime.PathProvider and discard
// it (`_ = paths`), while the adapter it returned called
// productionProviderDeps() and therefore always resolved the DEFAULT data
// directory. Under a custom CASCADE_HOME that means the provider-health
// check silently reported on a different store than every other provider
// subcommand. It was found by the redirected-HOME hygiene lane, which
// noticed real SQLite files appearing under a HOME that is asserted to
// stay empty, not by anything that was actually checking the behavior.
//
// The hygiene lane catches the symptom in CI. This catches the cause here.

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestProviderHealthSourceUsesTheInjectedPaths drives the real adapter
// against an injected PathProvider rooted in a temp dir and asserts the
// store it opens lands THERE. The assertion is the created file's
// location, not a mock call: a stubbed PathProvider that was still ignored
// would produce a passing test against a nil check, which is exactly the
// shape of the defect this replaces.
func TestProviderHealthSourceUsesTheInjectedPaths(t *testing.T) {
	paths := doctorTestPaths(t)
	dataDir := paths.DataDir()
	if dataDir == "" {
		t.Fatal("precondition: the injected PathProvider resolved no data directory")
	}

	src := providerHealthSourceFor(paths)
	if _, err := src.ListProviderHealth(context.Background()); err != nil {
		t.Fatalf("ListProviderHealth against the injected paths: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dataDir, "providers.db")); err != nil {
		t.Fatalf("the provider store was not opened under the INJECTED data directory %s: %v\n\n"+
			"providerHealthSourceFor must honor the PathProvider it is given. Discarding it makes "+
			"doctor report on the default store while every other provider subcommand reads the "+
			"configured one.", dataDir, err)
	}
}

// TestProviderHealthSourceLeavesNothingOutsideItsDataDir is the hygiene
// half: the adapter must not create anything in the real HOME. HOME is
// redirected to a temp dir here, so a regression writes there and this
// test sees it, rather than the failure only surfacing in the CI lane
// that asserts a redirected HOME stays empty.
func TestProviderHealthSourceLeavesNothingOutsideItsDataDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	src := providerHealthSourceFor(doctorTestPaths(t))
	if _, err := src.ListProviderHealth(context.Background()); err != nil {
		t.Fatalf("ListProviderHealth: %v", err)
	}

	entries, err := os.ReadDir(home)
	if err != nil {
		t.Fatalf("reading the redirected HOME: %v", err)
	}
	for _, e := range entries {
		t.Errorf("the provider-health source created %q under HOME; it must write only under the "+
			"data directory its PathProvider resolves", e.Name())
	}
}
