// Purpose: shortCascadeHome, shared by every cmd/cascade test that needs a
//
//	CASCADE_HOME a unix socket can actually be bound under.
//
// Constraints: this helper lives in its OWN untagged file deliberately. It
//
//	was originally declared in root_daemon_run_test.go, which had to gain
//	//go:build !windows because it drives socketDialable (a unix-only
//	symbol). Tagging that file also removed this helper from the Windows
//	build and broke chat_wiring_test.go, which is platform-neutral and
//	still needs it. A shared helper must not inherit one caller's platform
//	constraint, so it is split out here rather than duplicated.
//
// SPORT: cmd/cascade — test helper only, no exported surface.
package main

import (
	"os"
	"testing"
)

// shortCascadeHome mirrors newRunTestDeps' os.MkdirTemp choice: a real unix
// socket gets bound under this directory, and t.TempDir()'s long,
// test-name-embedding path can overflow sockaddr_un on darwin.
func shortCascadeHome(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "cascd")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}
