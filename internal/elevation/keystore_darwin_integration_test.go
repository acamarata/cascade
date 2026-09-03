//go:build darwin && cgo && integration

// Purpose: Art.2 real-counterpart integration test for the darwin
//
//	keystore: exercises the REAL Security.framework Keychain calls
//	(cascade_keychain_store / cascade_keychain_load_plain), never a
//	self-authored stub. See testdata/README.md for exactly what this
//	test does and does not prove, and this ticket's journal for the
//	honest account of what could not be exercised in the sandbox this
//	ticket was built in.
//
// Constraints: this test deliberately never calls Sign() — Sign triggers
//
//	a real, blocking LocalAuthentication (Touch ID/passcode) system
//	prompt with no human present to answer it in an unattended run, and
//	writing to a developer's real login Keychain from an automated CI
//	job with no cleanup path is its own hazard. Both are documented,
//	unresolved gaps, not silently skipped ones.
//
// SPORT: internal/elevation darwin integration test/ADDED (P1-E04-W1-S07-T6).
package elevation

import (
	"os"
	"testing"
)

func TestDarwinKeystore_RealSecurityFramework_NonInteractive(t *testing.T) {
	if os.Getenv("CI_SKIP_BIOMETRICS") != "1" {
		t.Skip("set CI_SKIP_BIOMETRICS=1 to run the non-interactive portion of this test " +
			"(GenerateKey/PubKeyB64 against the real Keychain); the interactive Sign/" +
			"LAContext.evaluatePolicy path is never exercised by automation — see testdata/README.md")
	}
	if os.Getenv("CASCADE_INTEGRATION_WRITE_KEYCHAIN") != "1" {
		t.Skip("this test writes a real item to the current user's login Keychain; " +
			"set CASCADE_INTEGRATION_WRITE_KEYCHAIN=1 to acknowledge that and run it")
	}

	ks := NewKeystore()
	if !ks.IsAvailable() {
		t.Skip("LocalAuthentication policy cannot be evaluated on this host " +
			"(no passcode/biometrics configured) — recorded as an untested platform gap, see testdata/README.md")
	}
	if err := ks.GenerateKey(); err != nil {
		t.Fatalf("GenerateKey against the real Keychain: %v", err)
	}
	pub, err := ks.PubKeyB64()
	if err != nil {
		t.Fatalf("PubKeyB64 against the real Keychain: %v", err)
	}
	if pub == "" {
		t.Fatal("PubKeyB64 returned an empty key after GenerateKey succeeded")
	}
	t.Log("Sign()/LAContext.evaluatePolicy is intentionally NOT exercised here: it would " +
		"block on a real system authentication prompt with no human present. See testdata/README.md.")
}
