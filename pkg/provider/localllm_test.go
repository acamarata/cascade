// Purpose: the allowed-fail seam test for LocalLLMSidecarProvider
//   (localllm.go): TestLocalLLMSidecarSeam probes for a sidecar binary at a
//   configured path and reports an explicit skip reason when none is
//   present - the expected outcome throughout P1, since the sidecar binary
//   is an explicit post-P1 artifact (Art.1.3) that this ticket does not
//   build or ship. No "net"/"net/http" import (Art.7.2): a live unix-socket
//   round trip needs a transport that differs by GOOS, and this file has no
//   sibling to carry a platform build tag (files_scope fixes exactly two
//   files for this seam), so the "binary present" branch fails loudly
//   rather than fabricating a round trip it cannot honestly perform here.
// SPORT: pkg.provider.localllm-sidecar-seam/ADD (P1-E10-W3-S19-T5).

package provider

import (
	"context"
	"os"
	"testing"
)

// sidecarBinaryEnv names the environment variable this test reads for the
// configured sidecar binary path. It is test-local, not a seam export: the
// production configuration surface for a future sidecar's binary path
// belongs to whichever ticket wires LocalLLMSidecarProvider into the
// daemon, not to this contract.
const sidecarBinaryEnv = "CASCADE_LOCALLLM_SIDECAR_BIN"

// fakeSidecar is a minimal in-process LocalLLMSidecarProvider, used only to
// prove the interface's method set is implementable and to give
// LocalLLMSidecarHealth a code reference beyond its own declaration.
type fakeSidecar struct {
	started bool
}

func (f *fakeSidecar) Start(_ context.Context, _ string) error {
	f.started = true
	return nil
}

func (f *fakeSidecar) Stop(_ context.Context) error {
	f.started = false
	return nil
}

func (f *fakeSidecar) Health(_ context.Context) (LocalLLMSidecarHealth, error) {
	if !f.started {
		return LocalLLMSidecarHealth{Ready: false, Detail: "not started; would probe " + LocalLLMSidecarHealthPath}, nil
	}
	return LocalLLMSidecarHealth{Ready: true, Detail: "would dispatch through " + LocalLLMSidecarRPCPath}, nil
}

var _ LocalLLMSidecarProvider = (*fakeSidecar)(nil)

// TestFakeSidecarLifecycle exercises the interface shape in-process (no
// subprocess, no socket): it is not the allowed-fail live probe below, only
// a compile-time-plus-behavior proof that the three lifecycle methods
// compose the way callers will expect.
func TestFakeSidecarLifecycle(t *testing.T) {
	ctx := context.Background()
	sc := &fakeSidecar{}
	if h, err := sc.Health(ctx); err != nil || h.Ready {
		t.Fatalf("Health before Start = %+v, %v; want not-ready, nil error", h, err)
	}
	if err := sc.Start(ctx, "/tmp/does-not-matter.sock"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if h, err := sc.Health(ctx); err != nil || !h.Ready {
		t.Fatalf("Health after Start = %+v, %v; want ready, nil error", h, err)
	}
	if err := sc.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

// probeSidecarBinary reports whether an executable file exists at path.
func probeSidecarBinary(path string) bool {
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	return info.Mode()&0o111 != 0
}

// TestLocalLLMSidecarSeam is the allowed-fail seam integration test (06 §5
// rule 19 precedent): it probes CASCADE_LOCALLLM_SIDECAR_BIN for a real
// sidecar binary. Throughout P1 no such binary exists anywhere in this
// tree or in CI, so this test skips with an explicit reason on every run -
// that skip is the expected, honest outcome per Art.1.3's post-P1
// deferral, never a silent pass standing in for a real-counterpart proof.
//
// If a path IS configured and a file exists there, this test deliberately
// fails rather than performing a fabricated or best-effort round trip: the
// live unix-socket handshake (R-14.36: HTTP/1.1 POST LocalLLMSidecarRPCPath
// + GET LocalLLMSidecarHealthPath) needs a transport whose implementation
// differs by GOOS (the standard "net" package's unix-socket support is not
// uniform across the build-test matrix's darwin/linux/windows runners, and
// this seam's files_scope fixes exactly this file plus localllm.go - no
// platform-tagged sibling to carry GOOS-specific dialing code). Failing
// loudly here is the honest choice over asserting a round trip this ticket
// cannot honestly perform; wiring the live dial is the job of whichever
// ticket ships the real sidecar binary and can add the transport file it
// needs.
func TestLocalLLMSidecarSeam(t *testing.T) {
	path := os.Getenv(sidecarBinaryEnv)
	if !probeSidecarBinary(path) {
		t.Skipf("skip: no localllm sidecar binary found (set %s to a real path to run this lane); "+
			"the sidecar binary is an explicit post-P1 artifact (04-PEWS-PLAN-W1-W3.md "+
			"§Wave 3 §Epic J S-19.T5 DECIDED) and does not ship in this ticket", sidecarBinaryEnv)
		return
	}
	t.Fatalf("a sidecar binary was found at %q (%s), but this ticket's seam does not implement the "+
		"live unix-socket round trip (see this test's doc comment); wire the real handshake when a "+
		"post-P1 sidecar binary needs verifying", path, sidecarBinaryEnv)
}
