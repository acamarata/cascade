//go:build !windows

// Purpose: error and edge branches of the helper keystore's path checks, its
//
//	self-signature read, and the production exec runner's socket, listener
//	and peer-pid plumbing, driven without a real keychain.
//
// SPORT: internal/elevation helper edge tests/ADDED (P1-SHIP-15).
package elevation

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

func resolveHelper(t *testing.T, exe func() (string, error)) (string, error) {
	t.Helper()
	return newHelperKeystoreWith(exe, nil, OKVerifier{}).resolve()
}

func TestHelperResolveRefusesWhatItCannotTrust(t *testing.T) {
	errExe := errors.New("no executable")
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T) func() (string, error)
		want  string
	}{
		{"executable lookup fails", func(*testing.T) func() (string, error) {
			return func() (string, error) { return "", errExe }
		}, "cannot resolve the cascade executable"},
		{"helper is not executable", func(t *testing.T) func() (string, error) {
			dir, exe := install(t, false)
			copyMode(t, exe, filepath.Join(dir, HelperName), 0o644)
			return func() (string, error) { return exe, nil }
		}, "not executable"},
		{"helper owner is untrusted", func(t *testing.T) func() (string, error) {
			dir, exe := install(t, false)
			writeFileForTest(t, filepath.Join(dir, HelperName))
			old := platformFileOwnerTrusted
			platformFileOwnerTrusted = func(os.FileInfo) bool { return false }
			t.Cleanup(func() { platformFileOwnerTrusted = old })
			return func() (string, error) { return exe, nil }
		}, "owned by another user"},
		{"no directory-chain check on this platform", func(t *testing.T) func() (string, error) {
			dir, exe := install(t, false)
			writeFileForTest(t, filepath.Join(dir, HelperName))
			old := platformDirChain
			platformDirChain = nil
			t.Cleanup(func() { platformDirChain = old })
			return func() (string, error) { return exe, nil }
		}, "unsupported on this platform"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path, err := resolveHelper(t, tc.setup(t))
			if path != "" {
				t.Fatalf("resolved %q despite the refusal", path)
			}
			wantHelperKind(t, err, cascade.KindUnavailable)
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not mention %q", err, tc.want)
			}
		})
	}
	if _, err := resolveHelper(t, func() (string, error) { return "", errExe }); !errors.Is(err, errExe) {
		t.Fatalf("the lookup failure is not wrapped: %v", err)
	}
}

func TestSelfTeamNeedsATeamAndACodeDirectoryHash(t *testing.T) {
	for _, tc := range []struct {
		name string
		dv   string
	}{
		{"no team identifier", "CDHash=" + cdHash + "\n"},
		{"no code directory hash", "TeamIdentifier=" + teamA + "\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &codesignScript{dv: tc.dv}
			team, err := c.verifier().SelfTeam()
			if team != "" {
				t.Fatalf("team = %q", team)
			}
			wantHelperKind(t, err, cascade.KindIntegrity)
			if len(c.calls) != 1 || c.calls[0][0] != "-dvvv" {
				t.Fatalf("a signature without %s must stop before --verify: %v", tc.name, c.calls)
			}
		})
	}
}

func TestExecHelperRefusesAProgramThatCannotStartOrNeverConnects(t *testing.T) {
	TrustAnyVolumeForTest(t)
	vets := 0
	vet := func(int) error { vets++; return nil }
	if _, err := execHelper(context.Background(), filepath.Join(t.TempDir(), "absent"), []byte("{}"), time.Second, vet); !errors.Is(err, os.ErrNotExist) || vets != 0 {
		t.Fatalf("missing program: err=%v vets=%d", err, vets)
	}
	script := filepath.Join(t.TempDir(), "idle")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexec /bin/sleep 5\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if _, err := execHelper(context.Background(), script, []byte("{}"), 500*time.Millisecond, vet); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("a helper that never connects: err=%v", err)
	}
	if vets != 1 || time.Since(start) > 4*time.Second {
		t.Fatalf("vets=%d after %v", vets, time.Since(start))
	}
}

func TestListenPrivateRefusesAnUnusableTempDirectory(t *testing.T) {
	TrustAnyVolumeForTest(t)
	base := resolvedTempDir(t)
	t.Setenv("TMPDIR", filepath.Join(base, "missing"))
	if _, _, err := listenPrivate(); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing temp dir: err=%v", err)
	}
	if os.Geteuid() == 0 {
		t.Skip("root can create entries in a read-only directory")
	}
	ro := filepath.Join(base, "ro")
	if err := os.Mkdir(ro, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(ro, 0o700) })
	t.Setenv("TMPDIR", ro)
	if _, _, err := listenPrivate(); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("read-only temp dir: err=%v", err)
	}
}

func TestSocketQuietSeesBytesAndAClosedPeer(t *testing.T) {
	if socketQuiet(nil) {
		t.Fatal("a nil gate reported quiet")
	}
	parent, child, err := socketpair()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = parent.Close() }()
	if !socketQuiet(parent) {
		t.Fatal("a fresh gate was not quiet")
	}
	if _, err := child.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if socketQuiet(parent) {
		t.Fatal("a gate holding bytes reported quiet")
	}
	parent2, child2, err := socketpair()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = parent2.Close() }()
	_ = child2.Close()
	if socketQuiet(parent2) {
		t.Fatal("a gate whose peer closed reported quiet")
	}
	_ = child.Close()
}

func TestPlatformFileOwnerTrustedNeedsAStatOwnedByUsOrRoot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !platformFileOwnerTrusted(fi) {
		t.Fatal("a file we own was not trusted")
	}
	if platformFileOwnerTrusted(fakeFileInfo{FileInfo: fi, sys: nil}) {
		t.Fatal("a file with no stat was trusted")
	}
}
