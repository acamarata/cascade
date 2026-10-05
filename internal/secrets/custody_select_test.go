//go:build darwin

package secrets

// Purpose: SelectCustody never downgrades to the file vault because the
//   platform probe could not clean up (CX2-002), while a keychain that
//   refused the write itself keeps its documented file-vault fallback.
// Constraints: platform custody only through Config.Runner (probeScript
//   over fakeSecurity) with an explicit fake keychain; HOME and USERPROFILE
//   are fresh temp dirs, and the file vault directory is inside one of them.

import (
	"io/fs"
	"path/filepath"
	"testing"
)

// selectIn runs SelectCustody with a working file-vault config rooted in
// home, whose directory does not exist yet, so anything written under home
// is the observable sign that selection fell back to the file vault.
func selectIn(t *testing.T, home string, p *probeScript) (Custody, error) {
	t.Helper()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	return SelectCustody(Config{
		Service: "cascade-select-test", Dir: filepath.Join(home, "cascade-data"),
		Passphrase: "select-test-pass" + "phrase", Runner: p.run, KeychainPath: fakeKeychainPath,
	})
}

// onDisk lists every path under home, home itself excluded.
func onDisk(t *testing.T, home string) []string {
	t.Helper()
	var found []string
	err := filepath.WalkDir(home, func(path string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path != home {
			found = append(found, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", home, err)
	}
	return found
}

// TestSelectCustodyNoFileFallbackOnProbeCleanupFailure covers every way a
// cleanup can fail after a successful probe write. Each is refused with the
// exact sentinel, twice (a caller that asks again, hoping for the file
// vault, gets the same refusal), and nothing is written under the temp HOME.
func TestSelectCustodyNoFileFallbackOnProbeCleanupFailure(t *testing.T) {
	scenarios := map[string]func() *probeScript{
		"permission": func() *probeScript {
			return newProbeScript(permissionStderr, permissionStderr, permissionStderr, permissionStderr)
		},
		"transient": func() *probeScript {
			return newProbeScript(transientStderr, transientStderr, transientStderr, transientStderr)
		},
		"spoofed not-found": func() *probeScript {
			return newProbeScript(spoofStderr, spoofStderr, spoofStderr, spoofStderr)
		},
		"post-delete find fails with a non-not-found error": func() *probeScript {
			p := newProbeScript()
			p.keepItem, p.findStderr = true, findDeniedStderr
			return p
		},
		"item survives a clean delete": func() *probeScript {
			p := newProbeScript()
			p.keepItem = true
			return p
		},
	}
	for name, mk := range scenarios {
		t.Run(name, func(t *testing.T) {
			p, home := mk(), t.TempDir()
			for attempt := 1; attempt <= 2; attempt++ {
				c, err := selectIn(t, home, p)
				if c != nil {
					t.Fatalf("attempt %d selected %q although the probe could not clean up", attempt, c.Name())
				}
				requireProbeCleanupFailed(t, err)
				if found := onDisk(t, home); len(found) != 0 {
					t.Fatalf("attempt %d wrote under the temp HOME (file-vault fallback): %v", attempt, found)
				}
			}
			// Both probes reached the injected runner: the refusal came from
			// the scripted keychain, not from some other path.
			if p.sets != 2 {
				t.Fatalf("the platform probe ran %d writes through the injected runner, want 2", p.sets)
			}
			if len(probeItems(p.fake)) == 0 {
				t.Fatal("scenario broken: the probe item is gone, so a refusal proves nothing")
			}
		})
	}
}

// TestSelectCustodyFallsBackWhenTheProbeWriteFails is the control that
// makes the absence check above able to fail: the same config, with the
// keychain refusing the WRITE (locked, headless), does write the vault
// under the temp HOME and selects the file vault, as R-14.270 documents.
func TestSelectCustodyFallsBackWhenTheProbeWriteFails(t *testing.T) {
	p, home := newProbeScript(lockedStderr, lockedStderr), t.TempDir()
	p.fake.fail["add-generic-password"] = lockedStderr
	c, err := selectIn(t, home, p)
	if err != nil {
		t.Fatalf("SelectCustody: %v", err)
	}
	if c.Name() != fileVaultName {
		t.Fatalf("selected %q, want the file vault", c.Name())
	}
	if len(onDisk(t, home)) == 0 {
		t.Fatal("the file vault fallback left nothing under the temp HOME; the absence check above is vacuous")
	}
}

// TestSelectCustodyPrefersAWorkingKeychain: a clean probe still selects the
// keychain and leaves the file vault untouched.
func TestSelectCustodyPrefersAWorkingKeychain(t *testing.T) {
	p, home := newProbeScript(), t.TempDir()
	c, err := selectIn(t, home, p)
	if err != nil || c.Name() != darwinCustodyName {
		t.Fatalf("SelectCustody = %v, %v; want the keychain", c, err)
	}
	if found := onDisk(t, home); len(found) != 0 {
		t.Fatalf("the file vault was written beside a working keychain: %v", found)
	}
	if len(probeItems(p.fake)) != 0 {
		t.Fatal("the probe item survived a clean probe")
	}
}

// TestSelectCustodyLockedKeychainKeepsFileVaultFallback is the R-14.270
// regression for P1-SEC-42: a keychain that refuses every probe call fast
// (locked, headless) is neither a timeout nor a cleanup failure, so it
// still selects the file vault.
func TestSelectCustodyLockedKeychainKeepsFileVaultFallback(t *testing.T) {
	p, home := newProbeScript(), t.TempDir()
	for _, sub := range []string{"add-generic-password", "delete-generic-password", "find-generic-password"} {
		p.fake.fail[sub] = lockedStderr
	}
	c, err := selectIn(t, home, p)
	if err != nil || c == nil || c.Name() != fileVaultName {
		t.Fatalf("SelectCustody = %v, %v; want the file vault with a nil error", c, err)
	}
	if len(onDisk(t, home)) == 0 {
		t.Fatal("the file vault fallback left nothing under the temp HOME")
	}
	plat, err := platformCustody(Config{Service: "cascade-select-test", Runner: p.run, KeychainPath: fakeKeychainPath})
	if err != nil {
		t.Fatalf("platformCustody: %v", err)
	}
	if perr := probePlatform(plat); perr == nil || perr == ErrProbeTimeout || perr == ErrProbeCleanupFailed {
		t.Fatalf("probePlatform = %v, want a plain unavailable", perr)
	}
}
