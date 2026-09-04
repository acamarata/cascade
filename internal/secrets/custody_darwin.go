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
	"encoding/json"
	"errors"
	"strings"
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
}

// platformCustody builds the darwin backend. It never fails at
// construction: availability is a runtime probe, so a host without the
// security tool selects the file vault instead of erroring at startup.
func platformCustody(cfg Config) (Custody, error) {
	return &keychainCustody{service: cfg.Service, run: cfg.runner()}, nil
}

// platformElevatedRefusal reports the platform-wide refusal of elevated
// vault verbs. macOS is a tier-1 platform, so there is none.
func platformElevatedRefusal() error { return nil }

// Name reports the backend label used in diagnostics.
func (k *keychainCustody) Name() string { return darwinCustodyName }

// Available probes the security tool with a harmless subcommand. It reports
// false rather than guessing when the probe cannot run at all.
func (k *keychainCustody) Available() bool {
	_, err := k.run(context.Background(), securityBin, "list-keychains")
	return err == nil
}

func (k *keychainCustody) account(name string) string { return keychainAccountPrefix + name }

// Set writes a generic password, replacing any existing entry (-U), with
// the value passed as hex through -X so plaintext never enters argv.
func (k *keychainCustody) Set(ctx context.Context, name string, value []byte) error {
	if err := validateSecretName(name); err != nil {
		return err
	}
	_, err := k.run(ctx, securityBin, "add-generic-password",
		"-a", k.account(name), "-s", k.service, "-U", "-X", hex.EncodeToString(value))
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
	out, err := k.run(ctx, securityBin, "find-generic-password",
		"-a", k.account(name), "-s", k.service, "-w")
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
	_, err := k.run(ctx, securityBin, "delete-generic-password",
		"-a", k.account(name), "-s", k.service)
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

// indexAccount is the account attribute of the name-index entry. It is
// namespaced away from the secret accounts so it can never collide with a
// stored name (validateSecretName rejects ':').
const indexAccount = keychainAccountPrefix + "index:names"

// readIndex loads the stored name list. A missing index is an empty vault;
// an index that will not decode is an integrity refusal, never a silently
// empty list, because reporting "no secrets" for a vault that has them
// invites a caller to overwrite them.
func (k *keychainCustody) readIndex(ctx context.Context) ([]string, error) {
	out, err := k.run(ctx, securityBin, "find-generic-password",
		"-a", indexAccount, "-s", k.service, "-w")
	if err != nil {
		if isKeychainNotFound(err) {
			return []string{}, nil
		}
		return nil, ErrCustodyUnavailable(darwinCustodyName, redactRunner(err))
	}
	raw, err := decodeKeychainValue(out)
	if err != nil {
		return nil, err
	}
	var names []string
	if uerr := json.Unmarshal(raw, &names); uerr != nil {
		return nil, ErrCustodyCorrupt(darwinCustodyName, errors.New("the keychain name index is not a valid name list"))
	}
	return sortedNames(names), nil
}

// writeIndex replaces the stored name list.
func (k *keychainCustody) writeIndex(ctx context.Context, names []string) error {
	encoded, err := json.Marshal(sortedNames(names))
	if err != nil {
		return ErrCustodyUnavailable(darwinCustodyName, err)
	}
	if _, err := k.run(ctx, securityBin, "add-generic-password",
		"-a", indexAccount, "-s", k.service, "-U", "-X", hex.EncodeToString(encoded)); err != nil {
		return ErrCustodyUnavailable(darwinCustodyName, redactRunner(err))
	}
	return nil
}

// indexAdd and indexRemove keep the name index in step with the entries.
func (k *keychainCustody) indexAdd(ctx context.Context, name string) error {
	names, err := k.readIndex(ctx)
	if err != nil {
		return err
	}
	for _, existing := range names {
		if existing == name {
			return nil
		}
	}
	return k.writeIndex(ctx, append(names, name))
}

func (k *keychainCustody) indexRemove(ctx context.Context, name string) error {
	names, err := k.readIndex(ctx)
	if err != nil {
		return err
	}
	kept := make([]string, 0, len(names))
	for _, existing := range names {
		if existing != name {
			kept = append(kept, existing)
		}
	}
	return k.writeIndex(ctx, kept)
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
