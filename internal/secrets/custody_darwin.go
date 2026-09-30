//go:build darwin

// Purpose: the macOS custody backend - generic passwords in the login
// keychain, reached by running /usr/bin/security as a subprocess.
// Inputs: a Config carrying the keychain service label and (in tests) an
// injected command runner.
// Outputs: a Custody backed by the real user keychain.
// Constraints: no CGO and no Security.framework linkage, so this backend
// survives the CGO_ENABLED=0 release build. Secret values reach
// /usr/bin/security as hex through -X, never as plaintext argv; nothing
// here logs, and no error carries a value.
//
// SPORT: internal/secrets Custody/ADDED (darwin keychain).

package secrets

import (
	"context"
	"encoding/hex"
	"errors"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	darwinCustodyName = "macos-keychain"
	securityBin       = "/usr/bin/security"
	// keychainAccountPrefix namespaces this vault's account attribute so a
	// List can enumerate exactly the entries cascade filed, and never the
	// user's unrelated keychain items.
	keychainAccountPrefix = "cascade-vault:"
)

// keychainCustody talks to /usr/bin/security. The service label separates
// one cascade profile (or one test run) from another.
type keychainCustody struct {
	service string
	run     commandRunner
	// stat checks that a resolved keychain path exists.
	stat func(string) (os.FileInfo, error)
	// configured is Config.KeychainPath: an explicit keychain that skips
	// the default lookup and its existence check entirely.
	configured string

	// The resolved keychain, computed once. Every security call passes it
	// as the trailing keychain argument, so none of them consults the
	// search list and none of them can raise a dialog (R-14.260).
	once         sync.Once
	keychainPath string
	keychainErr  error
}

// platformCustody builds the darwin backend. It never fails at
// construction: availability is a runtime probe, so a host without the
// security tool selects the file vault instead of erroring at startup.
func platformCustody(cfg Config) (Custody, error) {
	return &keychainCustody{
		service: cfg.Service, run: cfg.runner(), stat: os.Stat, configured: cfg.KeychainPath,
	}, nil
}

// platformElevatedRefusal reports the platform-wide refusal of elevated
// vault verbs. macOS is a tier-1 platform, so there is none.
func platformElevatedRefusal() error { return nil }

// Name reports the backend label used in diagnostics.
func (k *keychainCustody) Name() string { return darwinCustodyName }

// resolveKeychain returns the explicit path of the user's default
// keychain, or an error when none resolves.
//
// R-14.260: /usr/bin/security raises a GUI modal whenever it is asked to
// write and no default keychain resolves on the search list, which is the
// case under any redirected HOME (fake-home tests, service accounts, a
// fresh account before first login); one hung a whole test package. So the
// path is resolved once, checked to exist, and passed as the trailing
// keychain argument to every security call. With no path nothing is
// invoked: custody reports unavailable and SelectCustody lands on the
// encrypted file vault. There is no code path from here to a dialog.
func (k *keychainCustody) resolveKeychain(ctx context.Context) (string, error) {
	k.once.Do(func() {
		if k.configured != "" {
			k.keychainPath = k.configured
			return
		}
		out, err := k.run(ctx, securityBin, "default-keychain", "-d", "user")
		if err != nil {
			k.keychainErr = ErrCustodyUnavailable(darwinCustodyName, redactRunner(err))
			return
		}
		path := strings.Trim(strings.TrimSpace(string(out)), `"`)
		if path == "" {
			k.keychainErr = ErrCustodyUnavailable(darwinCustodyName,
				errors.New("no default user keychain is configured"))
			return
		}
		if k.stat != nil {
			if _, statErr := k.stat(path); statErr != nil {
				k.keychainErr = ErrCustodyUnavailable(darwinCustodyName,
					errors.New("the default user keychain does not exist"))
				return
			}
		}
		k.keychainPath = path
	})
	return k.keychainPath, k.keychainErr
}

// Available reports whether this backend can hold a secret (probe says why
// not), by WRITING into a keychain proven to exist: list-keychains passes
// where writes fail (R-14.258 F6); an unresolved write prompts (R-14.260).
func (k *keychainCustody) Available() bool { return k.probe(context.Background()) == nil }

// probeTimeout bounds one probe, retry included.
const probeTimeout = 2 * time.Second

// probe writes the probe item and removes it. A failed write is a plain
// unavailable (nothing was written; a locked or headless keychain keeps its
// file-vault fallback). A write whose item cannot be proven gone is
// ErrProbeCleanupFailed: that keychain works, so SelectCustody refuses.
func (k *keychainCustody) probe(parent context.Context) error {
	ctx, cancel := context.WithTimeout(parent, probeTimeout)
	defer cancel()
	keychain, err := k.resolveKeychain(ctx)
	if err != nil {
		return err
	}
	_, setErr := k.run(ctx, securityBin, "add-generic-password",
		"-a", availabilityProbeAccount, "-s", k.service, "-U", "-X", hex.EncodeToString([]byte{0}), keychain)
	// Cleanup runs even after a failed write, so a probe never accumulates.
	cleanErr := k.removeProbe(ctx, keychain)
	switch {
	case setErr != nil:
		return ErrCustodyUnavailable(darwinCustodyName, redactRunner(setErr))
	case cleanErr != nil:
		return ErrProbeCleanupFailed
	}
	return nil
}

// removeProbe deletes the probe item, retrying exactly once with the same
// identity, then confirms it is gone. Only a genuine not-found is absence.
func (k *keychainCustody) removeProbe(ctx context.Context, keychain string) error {
	del := []string{"delete-generic-password", "-a", availabilityProbeAccount, "-s", k.service, keychain}
	_, err := k.run(ctx, securityBin, del...)
	if err != nil && !probeAbsent(err) {
		_, err = k.run(ctx, securityBin, del...)
	}
	if err != nil && !probeAbsent(err) {
		return err
	}
	// Checked even after a clean delete; find without -w reads no value.
	_, err = k.run(ctx, securityBin, "find-generic-password",
		"-a", availabilityProbeAccount, "-s", k.service, keychain)
	switch {
	case err == nil:
		return errors.New("the probe item is still stored")
	case probeAbsent(err):
		return nil
	}
	return err
}

// probeAbsent is isKeychainNotFound minus any stderr that also reports a
// denial: a refusal quoting the not-found phrase is not proof of absence.
func probeAbsent(err error) bool {
	var re *runnerError
	if !isKeychainNotFound(err) || !errors.As(err, &re) {
		return false
	}
	text := strings.ToLower(re.stderr)
	for _, denial := range []string{"not allowed", "not permitted", "denied", "permission", "locked", "passphrase"} {
		if strings.Contains(text, denial) {
			return false
		}
	}
	return true
}

// availabilityProbeAccount is the account the probe writes and deletes. It
// is namespaced like every other entry, so one left behind by a killed
// process is visible to List and removable by the normal verbs.
const availabilityProbeAccount = keychainAccountPrefix + availabilityProbeName

func (k *keychainCustody) account(name string) string { return keychainAccountPrefix + name }

// Set writes a generic password, replacing any existing entry (-U), with
// the value passed as hex through -X so plaintext never enters argv.
func (k *keychainCustody) Set(ctx context.Context, name string, value []byte) error {
	if err := validateSecretName(name); err != nil {
		return err
	}
	keychain, err := k.resolveKeychain(ctx)
	if err != nil {
		return err
	}
	_, err = k.run(ctx, securityBin, "add-generic-password",
		"-a", k.account(name), "-s", k.service, "-U", "-X", hex.EncodeToString(value), keychain)
	if err != nil {
		return ErrCustodyUnavailable(darwinCustodyName, redactRunner(err))
	}
	return k.indexAdd(ctx, name)
}

// Get reads a generic password back. -w prints the value on stdout, so the
// caller's stdout is the only place it ever exists; a failure is classified
// from the tool's exit status and stderr text, never by echoing output.
func (k *keychainCustody) Get(ctx context.Context, name string) ([]byte, error) {
	if err := validateSecretName(name); err != nil {
		return nil, err
	}
	keychain, err := k.resolveKeychain(ctx)
	if err != nil {
		return nil, err
	}
	out, err := k.run(ctx, securityBin, "find-generic-password",
		"-a", k.account(name), "-s", k.service, "-w", keychain)
	if err != nil {
		if isKeychainNotFound(err) {
			return nil, ErrSecretNotFound(name)
		}
		return nil, ErrCustodyUnavailable(darwinCustodyName, redactRunner(err))
	}
	return decodeKeychainValue(out)
}

// decodeKeychainValue turns `security -w` output back into the stored
// bytes. The tool prints hex for a value written with -X; a value written
// by some other tool comes back as raw text, which is accepted as-is rather
// than refused, since refusing would make a pre-existing keychain entry
// permanently unreadable.
func decodeKeychainValue(out []byte) ([]byte, error) {
	text := strings.TrimRight(string(out), "\n")
	if decoded, err := hex.DecodeString(text); err == nil && len(text)%2 == 0 {
		return decoded, nil
	}
	return []byte(text), nil
}

// Delete removes the entry, mapping the tool's not-found status to the
// taxonomy's not-found kind.
func (k *keychainCustody) Delete(ctx context.Context, name string) error {
	if err := validateSecretName(name); err != nil {
		return err
	}
	keychain, err := k.resolveKeychain(ctx)
	if err != nil {
		return err
	}
	_, err = k.run(ctx, securityBin, "delete-generic-password",
		"-a", k.account(name), "-s", k.service, keychain)
	switch {
	case err == nil:
		return k.indexRemove(ctx, name)
	case isKeychainNotFound(err):
		return ErrSecretNotFound(name)
	default:
		return ErrCustodyUnavailable(darwinCustodyName, redactRunner(err))
	}
}

// List enumerates this service's entries from the name index kept beside
// them (see indexAccount). /usr/bin/security offers no way to enumerate one
// service's items without `dump-keychain`, which prompts the user for
// keychain access once per stored item and can be asked to print secret
// data; an index entry avoids both, and it is the only entry List ever
// reads.
func (k *keychainCustody) List(ctx context.Context) ([]string, error) {
	return k.readIndex(ctx)
}

// isKeychainNotFound classifies the security tool's "not found" failure.
// It reads the tool's own stderr text; anything it cannot positively
// classify is NOT treated as not-found, so an unreadable keychain surfaces
// as unavailable rather than as a missing secret.
func isKeychainNotFound(err error) bool {
	var re *runnerError
	if !errors.As(err, &re) {
		return false
	}
	text := strings.ToLower(re.stderr)
	return strings.Contains(text, "could not be found") ||
		strings.Contains(text, "specified item could not be found") ||
		strings.Contains(text, "errsecitemnotfound")
}

// redactRunner strips a runnerError's captured stderr before the failure
// travels any further. The security tool's diagnostics can quote the
// arguments it was given, and Set's arguments include the (hex-encoded)
// value: a wrapped error is the one place that text could otherwise reach a
// log or a terminal.
func redactRunner(err error) error {
	var re *runnerError
	if errors.As(err, &re) {
		return errors.New("the /usr/bin/security invocation failed (diagnostics withheld: they can quote the value)")
	}
	return err
}
