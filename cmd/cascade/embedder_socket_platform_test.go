// Purpose: the local-sidecar embedder socket is a unix socket; windows
//
//	refuses it, so the tests that admit one skip there and the refusal has
//	its own test that runs everywhere.
//
// SPORT: cmd/cascade/embedder (tests).
package main

import (
	"path/filepath"
	goruntime "runtime"
	"testing"

	"github.com/acamarata/cascade/internal/retrieval/embedsel"
	"github.com/acamarata/cascade/pkg/cascade"
)

// unixSocketFixture is socketFixture for a test that admits a local sidecar socket: the
// embedder refuses every unix socket on windows (see
// TestComposeEmbedderRefusesSocketOnWindows), so there is nothing to admit.
func unixSocketFixture(t *testing.T, path string) {
	t.Helper()
	if goruntime.GOOS == "windows" {
		t.Skip("unix sockets are not supported on windows")
	}
	socketFixture(t, path)
}

func TestComposeEmbedderRefusesSocketOnWindows(t *testing.T) {
	paths := embedTestPaths(t)
	sock := filepath.Join(t.TempDir(), "s", "bgem3.sock")
	socketFixture(t, sock)
	deps := testDeps(paths, &recordingDoor{})
	deps.goos = "windows"
	sel := composeEmbedderWith(loadEmbedConfig(t, paths, laneBody), admittedSocket(t, sock), deps)
	if sel.Source() != embedsel.SourceLocalSidecar {
		t.Fatalf("source %s, want local-sidecar (no lane fallback)", sel.Source())
	}
	wantKind(t, sel.Err(), cascade.KindPolicyDenied, "unix sockets are not supported on windows")
}
