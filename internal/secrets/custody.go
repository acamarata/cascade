// Purpose: the Custody contract every platform secret store implements,
//
//	the fail-closed error vocabulary the vault broker and CLI share, and
//	the backend-selection order that picks a custody for this host.
//
// Inputs: a Config naming the service/collection label, the file-vault
//
//	directory, and (for tests) an injected entropy source and command
//	runner. Platform backends are constructed by the build-tagged
//	platformCustody in custody_darwin.go / custody_linux.go /
//	custody_windows.go.
//
// Outputs: the Custody interface, its typed errors, and SelectCustody.
// Constraints: no CGO anywhere (macOS shells out to /usr/bin/security,
//
//	linux speaks D-Bus in pure Go, every other platform gets the
//	encrypted file vault). Every method fails closed: a backend that
//	cannot prove a safe outcome returns a typed error, never a zero
//	value a caller could read as a real secret. No secret value is ever
//	placed in an error message, a log line, or a file path.
//
// SPORT: internal/secrets Custody/ADDED.

package secrets

import (
	"context"
	"crypto/rand"
	"io"
	"os/exec"
	"sort"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

// maxSecretNameLen bounds a secret name. The macOS keychain and the
// freedesktop secret service both accept far longer labels; the cap exists
// so a name can never be used to smuggle a payload through an attribute
// field, and so `vault list` output stays a list of names.
const maxSecretNameLen = 128

// Custody is one concrete secret store: an OS keychain, a session secret
// service, or the encrypted file vault. Implementations hold opaque byte
// values keyed by a validated name.
//
// Every method fails closed. An implementation that cannot reach its
// backing store returns a typed KindUnavailable error rather than an empty
// result; Get on a name that is not stored returns KindNotFound rather
// than an empty value.
type Custody interface {
	// Name identifies the backend for diagnostics and for the broker's
	// "which backend answered" reporting. It is never a secret.
	Name() string
	// Available reports whether this backend can be used on this host at
	// all. It never reports true on a guess: a backend that cannot probe
	// its dependency reports false and the selector moves on.
	Available() bool
	// Set stores value under name, overwriting any existing entry
	// (idempotent: a second Set of the same pair succeeds).
	Set(ctx context.Context, name string, value []byte) error
	// Get returns the stored value, or ErrSecretNotFound.
	Get(ctx context.Context, name string) ([]byte, error)
	// Delete removes name, or returns ErrSecretNotFound if it is absent.
	Delete(ctx context.Context, name string) error
	// List returns the stored names, sorted. It never returns values.
	List(ctx context.Context) ([]string, error)
}

// commandRunner runs an external program and returns its stdout. It is the
// seam the darwin backend uses so unit tests exercise every branch without
// a real keychain, and the integration lane runs the real /usr/bin/security.
type commandRunner func(ctx context.Context, name string, args ...string) ([]byte, error)

// execRunner is the production commandRunner: exec.CommandContext, with
// stderr captured into the error so a backend can CLASSIFY a failure (for
// example "could not be found in the keychain"). The captured text is
// never surfaced verbatim: the darwin backend strips it before wrapping,
// because the tool's diagnostics can quote the arguments it was given.
func execRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, &runnerError{err: err, stderr: stderr.String()}
	}
	return out, nil
}

// runnerError carries an external command's failure plus its stderr. The
// stderr text is available to backends that must classify a failure (for
// example "could not be found in the keychain") but is never surfaced to a
// caller verbatim.
type runnerError struct {
	err    error
	stderr string
}

func (e *runnerError) Error() string { return e.err.Error() }
func (e *runnerError) Unwrap() error { return e.err }

// Config carries every external input a custody backend needs. Nothing here
// is a secret value.
type Config struct {
	// Service is the keychain service / secret-service collection label
	// entries are filed under. Tests set a dedicated value so they never
	// touch the real user's vault entries.
	Service string
	// Dir is the directory the encrypted file vault lives in. Tests point
	// it at t.TempDir().
	Dir string
	// Passphrase is the file vault's unlock passphrase. When empty the
	// file vault derives one from a 0600 key file inside Dir.
	Passphrase string
	// Rand is the entropy source for the file vault. Nil means crypto/rand.
	Rand io.Reader
	// Runner runs external programs (darwin's /usr/bin/security). Nil means
	// the real exec.CommandContext runner.
	Runner commandRunner
}

func (c Config) rand() io.Reader {
	if c.Rand != nil {
		return c.Rand
	}
	return rand.Reader
}

func (c Config) runner() commandRunner {
	if c.Runner != nil {
		return c.Runner
	}
	return execRunner
}

// SelectCustody picks the custody backend for this host: the platform
// backend when it is available, otherwise the encrypted file vault. It
// never returns a nil Custody with a nil error.
//
// Selection is explicit, not silent: the returned Custody's Name() reports
// which backend answered, and the broker surfaces it, so a host that fell
// back to the file vault says so rather than pretending it used the OS
// keychain.
func SelectCustody(cfg Config) (Custody, error) {
	if cfg.Service == "" {
		return nil, cascade.New(cascade.KindInvalidInput, "secrets: custody config needs a service label")
	}
	if plat, err := platformCustody(cfg); err == nil && plat != nil && plat.Available() {
		return plat, nil
	}
	fv, err := newFileVaultCustody(cfg)
	if err != nil {
		return nil, err
	}
	if !fv.Available() {
		return nil, ErrNoCustodyAvailable()
	}
	return fv, nil
}

// validateSecretName is the fail-closed name check every backend and the
// broker apply before touching a store. A name it cannot positively accept
// is refused; nothing is normalised or guessed on the caller's behalf,
// because a silently-rewritten name reads back as a different secret.
func validateSecretName(name string) error {
	switch {
	case name == "":
		return cascade.New(cascade.KindInvalidInput, "secrets: secret name must not be empty")
	case len(name) > maxSecretNameLen:
		return cascade.Newf(cascade.KindInvalidInput,
			"secrets: secret name is longer than the %d-character limit", maxSecretNameLen)
	case strings.HasPrefix(name, "-"):
		return cascade.New(cascade.KindInvalidInput,
			"secrets: secret name must not start with '-' (it would parse as a command flag)")
	}
	for _, r := range name {
		if r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			continue
		}
		if r == '_' || r == '-' || r == '.' {
			continue
		}
		return cascade.Newf(cascade.KindInvalidInput,
			"secrets: secret name %q contains an unsupported character; use letters, digits, '_', '-' or '.'", name)
	}
	return nil
}

// sortedNames returns a sorted copy, the shape every List returns.
func sortedNames(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

// ErrSecretNotFound reports that no secret is stored under name.
// KindNotFound: the record a caller asked about does not exist. The name is
// safe to include; the value is not, and never is.
func ErrSecretNotFound(name string) error {
	return cascade.Newf(cascade.KindNotFound, "secrets: no secret named %q is stored", name)
}

// ErrSecretExists reports a name collision on a non-overwriting Set.
// KindConflict: a competing write against a uniquely-keyed record.
func ErrSecretExists(name string) error {
	return cascade.Newf(cascade.KindConflict, "secrets: a secret named %q already exists", name)
}

// ErrCustodyUnavailable reports that a backend's dependency (keychain
// daemon, session bus, vault directory) could not be reached.
// KindUnavailable: retryable once the dependency is back, but nothing is
// stored or read here today.
func ErrCustodyUnavailable(backend string, cause error) error {
	if cause == nil {
		return cascade.Newf(cascade.KindUnavailable, "secrets: the %s custody backend is not available on this host", backend)
	}
	return cascade.Wrapf(cascade.KindUnavailable, cause, "secrets: the %s custody backend is not available on this host", backend)
}

// ErrNoCustodyAvailable reports that neither a platform backend nor the
// encrypted file vault could be opened. KindUnavailable, and deliberately
// not a silent empty store: a vault that cannot be opened must refuse, not
// behave like an empty one.
func ErrNoCustodyAvailable() error {
	return cascade.New(cascade.KindUnavailable,
		"secrets: no secret custody backend is available (no OS keychain and no writable encrypted file vault)")
}

// ErrCustodyCorrupt reports that a store's contents could not be decoded.
// KindIntegrity, and it refuses rather than starting over: silently
// replacing an unreadable vault with an empty one destroys every secret in
// it.
func ErrCustodyCorrupt(backend string, cause error) error {
	return cascade.Wrapf(cascade.KindIntegrity, cause,
		"secrets: the %s custody store could not be decoded; refusing to overwrite it", backend)
}
