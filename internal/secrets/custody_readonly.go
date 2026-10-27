// Purpose: a custody selection for read-only callers (the doctor): it
//
//	answers "is this entry stored?" without the side effects SelectCustody
//	has (the platform probe writes and removes an item; the file vault
//	creates its directory and key file; Available writes a probe file).
//
// Inputs: the same Config SelectCustody takes.
// Outputs: SelectCustodyReadOnly.
// Constraints: nothing here writes. Set and Delete on the returned custody
//
//	refuse KindPermissionDenied; a file vault that was never initialised
//	reads as empty rather than being created.
//
// SPORT: internal/secrets SelectCustodyReadOnly/ADDED (P1-SEC-23).

package secrets

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/acamarata/cascade/pkg/cascade"
)

// SelectCustodyReadOnly picks the custody a read-only diagnostic should
// consult: the platform backend when it answers a listing (a read), else
// the file vault opened without creating anything.
func SelectCustodyReadOnly(cfg Config) (Custody, error) {
	if cfg.Service == "" {
		return nil, cascade.New(cascade.KindInvalidInput, "secrets: custody config needs a service label")
	}
	if !cfg.ForceFileVault {
		if plat, err := platformCustody(cfg); err == nil && plat != nil {
			if _, lerr := plat.List(context.Background()); lerr == nil {
				return readOnlyCustody{inner: plat}, nil
			}
		}
	}
	return readOnlyFileVault(cfg)
}

// readOnlyFileVault opens an existing file vault with its existing key, or
// reports an empty one when the directory or key file does not exist.
func readOnlyFileVault(cfg Config) (Custody, error) {
	if cfg.Dir == "" {
		return nil, cascade.New(cascade.KindInvalidInput, "secrets: the file vault needs a directory")
	}
	pass := cfg.Passphrase
	if pass == "" {
		raw, err := os.ReadFile(filepath.Join(cfg.Dir, fileVaultKeyName)) //nolint:gosec // composed from the configured vault dir
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return readOnlyCustody{inner: emptyCustody{}}, nil
		case err != nil:
			return nil, ErrCustodyUnavailable(fileVaultName, err)
		case len(raw) < 32:
			return nil, ErrCustodyCorrupt(fileVaultName, errors.New("vault key file is too short"))
		}
		pass = string(raw)
	}
	return readOnlyCustody{inner: &fileVaultCustody{dir: cfg.Dir, passphrase: pass, rnd: cfg.rand()}}, nil
}

// readOnlyCustody forwards reads and refuses writes.
type readOnlyCustody struct{ inner Custody }

// Name reports the wrapped backend.
func (r readOnlyCustody) Name() string { return r.inner.Name() }

// Available reports true without probing: a probe is a write.
func (r readOnlyCustody) Available() bool { return true }

// Get forwards to the wrapped backend.
func (r readOnlyCustody) Get(ctx context.Context, name string) ([]byte, error) {
	return r.inner.Get(ctx, name)
}

// List forwards to the wrapped backend.
func (r readOnlyCustody) List(ctx context.Context) ([]string, error) { return r.inner.List(ctx) }

// Set refuses: this custody is for diagnostics.
func (r readOnlyCustody) Set(context.Context, string, []byte) error { return errReadOnlyCustody() }

// Delete refuses: this custody is for diagnostics.
func (r readOnlyCustody) Delete(context.Context, string) error { return errReadOnlyCustody() }

func errReadOnlyCustody() error {
	return cascade.New(cascade.KindPermissionDenied, "secrets: this custody was opened read-only")
}

// emptyCustody is a file vault that was never initialised: nothing stored.
type emptyCustody struct{}

func (emptyCustody) Name() string                              { return fileVaultName }
func (emptyCustody) Available() bool                           { return true }
func (emptyCustody) Set(context.Context, string, []byte) error { return errReadOnlyCustody() }
func (emptyCustody) Delete(context.Context, string) error      { return errReadOnlyCustody() }
func (emptyCustody) List(context.Context) ([]string, error)    { return nil, nil }
func (emptyCustody) Get(_ context.Context, name string) ([]byte, error) {
	return nil, ErrSecretNotFound(name)
}
