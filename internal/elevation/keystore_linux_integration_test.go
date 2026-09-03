//go:build linux && cgo && pam && integration

// Purpose: Art.2 real-counterpart integration test for the linux
//
//	keystore: exercises the REAL PAM stack via pam_start/pam_authenticate
//	(cascade_pam_authenticate) and the kernel keyring syscalls
//	(add_key(2)/keyctl(2)), never a self-authored stub. See
//	testdata/README.md for provenance and exactly what this test proves.
//
// Constraints: this ticket was built entirely on a darwin host with no
//
//	linux machine or container available to it (see the ticket journal's
//	platform-coverage section) — this file compiles by inspection against
//	the CGO preamble in keystore_linux.go, but was NEVER compiled or run
//	in this ticket's session. That is an honest, explicit gap, not a
//	silent one. CI's own linux runner is this test's first real
//	execution.
//
// SPORT: internal/elevation linux integration test/ADDED (P1-E04-W1-S07-T6).
package elevation

import (
	"os"
	"testing"
)

func TestLinuxKeystore_RealPAMAndKeyring(t *testing.T) {
	if os.Getenv("CI_HAS_PAM_PERMIT") != "1" {
		t.Skip("set CI_HAS_PAM_PERMIT=1 on a runner whose /etc/pam.d/cascade-elevate " +
			"(or /etc/pam.d/other) stacks pam_permit.so, so pam_authenticate succeeds " +
			"non-interactively; see testdata/README.md for the exact fixture stack this " +
			"test expects")
	}

	ks := NewKeystore()
	if !ks.IsAvailable() {
		t.Skip("no PAM stack configured for this service on this host — recorded gap, see testdata/README.md")
	}
	if err := ks.GenerateKey(); err != nil {
		t.Fatalf("GenerateKey against the real kernel keyring: %v", err)
	}
	pub, err := ks.PubKeyB64()
	if err != nil {
		t.Fatalf("PubKeyB64 against the real kernel keyring: %v", err)
	}
	if pub == "" {
		t.Fatal("PubKeyB64 returned an empty key after GenerateKey succeeded")
	}

	sig, err := ks.Sign([]byte("integration-test-payload"))
	if err != nil {
		t.Fatalf("Sign (real pam_authenticate against pam_permit.so + real keyring read): %v", err)
	}
	if len(sig) == 0 {
		t.Fatal("Sign returned an empty signature")
	}
}
