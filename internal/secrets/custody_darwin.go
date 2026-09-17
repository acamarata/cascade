//go:build darwin

// Purpose: the macOS custody backend - generic passwords in the login
//
//	keychain, reached by running /usr/bin/security as a subprocess.
//
// Inputs: a Config carrying the keychain service label and (in tests) an
//
//	injected command runner.
//
// Outputs: a Custody backed by the real user keychain.
// Constraints: no CGO and no Security.framework linkage, so this backend
//
//	survives the CGO_ENABLED=0 release build that every shipped binary is
//	made with. Secret values reach /usr/bin/security as hex through -X
//	rather than as plaintext argv, so a value never appears in the
//	process table; nothing here logs, and no error carries a value.
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
// R-14.260, after an incident: /usr/bin/security raises a GUI modal
// ("A keychain cannot be found to store ...") whenever it is asked to
// write and no default keychain resolves on the search list. A redirected
// HOME has no search list -- which is every fake-home test, the Art.7.1
// redirected-HOME lane, a daemon under a service account, and a fresh
// machine account before first login. A modal appeared on a real desktop
// and hung a whole test package.
//
// So this package never relies on the search list. The path is resolved
// once, checked to exist, and passed as the trailing keychain argument to
// every security call. With no path resolved nothing is invoked at all:
// custody reports unavailable and SelectCustody lands on the encrypted
// file vault. There is no code path from here to a dialog.
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

// Available reports whether this backend can hold a secret, by resolving
// the keychain it would write into.
//
// It used to run `security list-keychains`, which succeeds on a host where
// Set fails: with HOME pointed anywhere but the logged-in user's home,
// list-keychains still finds the System keychain and exits 0 while a write
// has no user keychain to go into. SelectCustody gates a write on this
// answer, so it chose a keychain it could not write to and never reached
// the file vault (R-14.258 Finding 6).
//
// It then, briefly, probed by WRITING -- a probe should exercise the
// capability it gates -- and that is what raised the modal. The write
// still happens, but only into a path already proven to resolve and
// exist, which is the state in which security has nothing to ask about.
func (k *keychainCustody) Available() bool {
	ctx := context.Background()
	keychain, err := k.resolveKeychain(ctx)
	if err != nil {
		return false
	}
	_, setErr := k.run(ctx, securityBin, "add-generic-password",
		"-a", availabilityProbeAccount, "-s", k.service, "-U", "-X", hex.EncodeToString([]byte{0}), keychain)
	// Deferred in spirit: the delete runs whether or not the write
	// reported success, so a probe can never accumulate.
	_, _ = k.run(ctx, securityBin, "delete-generic-password",
		"-a", availabilityProbeAccount, "-s", k.service, keychain)
	return setErr == nil
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
