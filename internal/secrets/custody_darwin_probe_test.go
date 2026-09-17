//go:build darwin

package secrets

// Purpose: the darwin custody's AVAILABILITY probe (P1-E16-W4-S35-T9) --
//   that it separates a host whose keychain can hold a secret from one
//   whose cannot, and that it never runs a subcommand which could prompt.
// Constraints: driven through the injected command runner, so no unit
//   test touches the real user keychain -- and, since this probe's first
//   correction raised a GUI dialog on a real desktop, the second
//   assertion below is about which subcommand runs at all.
// SPORT: internal/secrets Custody availability probe (ADD) -- P1-E16-W4-S35-T9.

import (
	"context"
	"errors"
	"testing"
)

// TestAvailableSeparatesAHostThatCanHoldASecret is the regression for
// R-14.258 Finding 6.
//
// The read-only runner is the real host state this was found in: a
// process whose HOME is not the logged-in user's -- a daemon under a
// service account, a container, every test that pins HOME -- where
// `list-keychains` still finds the System keychain and succeeds while no
// user keychain resolves and every write fails.
//
// Both halves are asserted. A probe that reported false for everything
// would pass the first half alone, and would move every real user's
// secrets out of their OS keychain on the next command.
func TestAvailableSeparatesAHostThatCanHoldASecret(t *testing.T) {
	kc, _ := newFakeKeychain(t)
	if !kc.Available() {
		t.Error("Available() = false on a host with a default user keychain")
	}

	fake := newFakeSecurity()
	kc2, err := platformCustody(Config{Service: "cascade-unit-test", Runner: noDefaultKeychain(fake)})
	if err != nil {
		t.Fatalf("platformCustody: %v", err)
	}
	if kc2.Available() {
		t.Error("Available() = true on a host where no default keychain resolves; " +
			"SelectCustody gates a WRITE on this answer, so the encrypted file vault is never reached")
	}
}

// TestNoDefaultKeychainIsUnavailableNotPrompt is the regression for the
// incident the first correction caused (R-14.260).
//
// Probing by WRITING was right about the principle -- a probe should
// exercise the capability it gates -- and wrong about the mechanics. With
// no default keychain on the search list, `add-generic-password` does not
// fail: /usr/bin/security raises a GUI authorization dialog and BLOCKS. It
// did so on a real desktop, and it hung every test in cmd/cascade, all of
// which run under a redirected HOME.
//
// The fix is not to stop writing. It is to never invoke security at all
// without a keychain already resolved and proven to exist. So the
// assertion is on the ARGV: with the lookup failing, no password
// subcommand may run, and the answer must be an unavailable that sends
// SelectCustody to the file vault.
func TestNoDefaultKeychainIsUnavailableNotPrompt(t *testing.T) {
	fake := newFakeSecurity()
	kc, err := platformCustody(Config{Service: "cascade-unit-test", Runner: noDefaultKeychain(fake)})
	if err != nil {
		t.Fatalf("platformCustody: %v", err)
	}
	if kc.Available() {
		t.Error("Available() = true with no default keychain resolved")
	}
	for _, call := range fake.calls {
		for _, arg := range call {
			switch arg {
			case "add-generic-password", "delete-generic-password", "find-generic-password":
				t.Errorf("security ran %q with no keychain resolved; that is the invocation "+
					"which raises a GUI authorization dialog, and it must never be reached", arg)
			}
		}
	}
}

// TestEverySecurityCallCarriesAnExplicitKeychain is the other half of
// R-14.260: relying on the default search list is what made a dialog
// possible at all, so no invocation may omit the keychain argument.
//
// Asserted over a real round trip rather than over Available alone,
// because the probe is not the only thing that writes -- Set, Get, Delete
// and the name index all do, and each was its own path to the search
// list.
func TestEverySecurityCallCarriesAnExplicitKeychain(t *testing.T) {
	kc, fake := newFakeKeychain(t)
	ctx := context.Background()
	if !kc.Available() {
		t.Fatal("Available() = false with a working security tool")
	}
	if err := kc.Set(ctx, "TOKEN", []byte("s3cr3t")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if _, err := kc.Get(ctx, "TOKEN"); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if _, err := kc.List(ctx); err != nil {
		t.Fatalf("List: %v", err)
	}
	if err := kc.Delete(ctx, "TOKEN"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	for _, call := range fake.calls {
		if len(call) < 2 {
			continue
		}
		switch call[1] {
		case "add-generic-password", "find-generic-password", "delete-generic-password":
			if call[len(call)-1] != fakeKeychainPath {
				t.Errorf("%q ran without an explicit keychain as its last argument: %v", call[1], call)
			}
		}
	}
}

// TestTheProbeLeavesNoItemBehind: the probe writes a real entry, so it has
// to remove it, whether or not the write reported success.
func TestTheProbeLeavesNoItemBehind(t *testing.T) {
	kc, fake := newFakeKeychain(t)
	if !kc.Available() {
		t.Fatal("Available() = false with a working security tool")
	}
	if _, present := fake.items[availabilityProbeAccount]; present {
		t.Error("the availability probe's entry survived the probe")
	}
}

// noDefaultKeychain wraps a fake so that the default-keychain lookup fails
// the way a host with a redirected HOME fails it, and everything else
// still answers.
func noDefaultKeychain(fake *fakeSecurity) commandRunner {
	return func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if len(args) > 0 && args[0] == "default-keychain" {
			return nil, errors.New("security: SecKeychainCopyDomainDefault user: " +
				"A default keychain could not be found.")
		}
		return fake.run(ctx, name, args...)
	}
}
