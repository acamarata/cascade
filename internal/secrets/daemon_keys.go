// Purpose: the daemon's own Ed25519 keys in the vault: provisioned once,
//
//	create-only, under names no vault verb may set, rename onto or delete.
//
// Inputs: a broker over the daemon's custody, a reserved key name and a
//
//	randomness source for the one-time generation.
//
// Outputs: InstallationKeyName, ErrReservedName, ProvisionDaemonKey.
// Constraints: FAIL CLOSED. An existing value is never overwritten: a value
//
//	of the wrong shape is KindIntegrity (zeroed, nothing generated or
//	written), and a read that fails for any reason other than KindNotFound is
//	returned unchanged with nothing generated. ApprovalKeyName itself is
//	declared in unelevated.go, where its reader lives.
//
// SPORT: internal/secrets ProvisionDaemonKey/ADDED, ErrReservedName/ADDED (P1-SEC-23).

package secrets

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/subtle"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// InstallationKeyName is the vault entry the daemon's installation identity
// key is stored under. Like ApprovalKeyName it is reserved.
const InstallationKeyName = "cascade_installation_key"

// ErrReservedName is the refusal for a vault verb aimed at a reserved daemon
// key name. Compare by message or with errors.Is on the wrapped chain: a
// cascade error's Is compares Kind only.
var ErrReservedName = cascade.New(cascade.KindPermissionDenied,
	"secrets: the name is a reserved daemon key")

// reservedDaemonKey reports whether name is one of the daemon's own keys.
func reservedDaemonKey(name string) bool {
	for _, r := range reservedDaemonKeyNames {
		if name == r {
			return true
		}
	}
	return false
}

// reservedDaemonKeyNames is every reserved daemon key. None may end in
// _<digits>: Set's rename mode stores under name_<n>, so such a name could
// be reached as a rename target (TestNoReservedNameIsARenameTarget).
var reservedDaemonKeyNames = []string{ApprovalKeyName, InstallationKeyName}

// refuseReservedName is the broker's guard for every mutating verb.
func refuseReservedName(name string) error {
	if !reservedDaemonKey(name) {
		return nil
	}
	return cascade.Wrapf(cascade.KindPermissionDenied, ErrReservedName,
		"secrets: %q is a reserved daemon key; no vault verb sets, renames onto or deletes it", name)
}

// provisionMu serialises provisioning inside one process; the file lock
// the broker names (WithDaemonKeyLock, acquireLockFile) serialises it
// across processes, so two daemons starting at once can never both see
// "absent" and both write: the check and the create are one critical
// section, which makes the create create-only.
var provisionMu sync.Mutex

// provisionYield runs between the absence check and the write. It is a
// no-op; the concurrent-provisioner test widens the window with it.
var provisionYield = func() {}

// lockContended runs each time acquireLockFile finds path held. A no-op;
// tests use it to wait deterministically for a writer to block.
var lockContended = func(string) {}

// daemonKeyLockWait bounds how long a writer waits for another.
const daemonKeyLockWait = 30 * time.Second

// WithDaemonKeyLock returns a broker over the same custody whose daemon-key
// provisioning takes the exclusive lock file at path. The caller scopes the
// path to the custody's identity (the vault directory for the file vault, a
// per-user location keyed by backend and service for a platform keystore),
// so every process that could write the same key takes the same lock.
// Provisioning on a broker without one refuses.
func (b *Broker) WithDaemonKeyLock(path string) *Broker {
	if b == nil {
		return nil
	}
	c := *b
	c.keyLockPath = path
	return &c
}

// acquireLockFile takes an exclusive lock on the file at path (0600, in a
// 0700 directory), polling until ctx or daemonKeyLockWait ends. The release
// unlocks and closes it; the OS also drops it if the process dies.
func acquireLockFile(ctx context.Context, path string) (func(), error) {
	if path == "" {
		return nil, cascade.New(cascade.KindUnavailable,
			"secrets: this write needs a cross-process lock and none is configured; refusing")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "secrets: preparing a vault lock")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600) //nolint:gosec // composed by the caller from its own directory
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "secrets: opening a vault lock")
	}
	ctx, cancel := context.WithTimeout(ctx, daemonKeyLockWait)
	defer cancel()
	for {
		ok, lerr := tryLockDaemonKey(f)
		switch {
		case lerr != nil:
			_ = f.Close()
			return nil, cascade.Wrap(cascade.KindUnavailable, lerr, "secrets: taking a vault lock")
		case ok:
			return func() { unlockDaemonKey(f); _ = f.Close() }, nil
		}
		lockContended(path)
		select {
		case <-ctx.Done():
			_ = f.Close()
			return nil, cascade.Wrap(cascade.KindUnavailable, ctx.Err(), "secrets: another process holds a vault lock")
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// ProvisionDaemonKey returns the key id and public half of the reserved key
// name, generating and storing it only when custody reports it absent.
// created reports that this call wrote it. The private half never leaves
// this function: it is zeroed before return on every path.
func ProvisionDaemonKey(ctx context.Context, b *Broker, name string, rnd io.Reader) (keyID string, pub ed25519.PublicKey, created bool, err error) {
	if !reservedDaemonKey(name) {
		return "", nil, false, cascade.Newf(cascade.KindInvalidInput,
			"secrets: %q is not a daemon key name", name)
	}
	if b == nil || b.custody == nil {
		return "", nil, false, ErrNoCustodyAvailable()
	}
	provisionMu.Lock()
	defer provisionMu.Unlock()
	release, err := acquireLockFile(ctx, b.keyLockPath)
	if err != nil {
		return "", nil, false, err
	}
	defer release()
	value, err := daemonKeyGet(ctx, b, name)
	switch {
	case err == nil:
		pub, err = daemonPublicKey(name, value)
		return keyIDFor(pub, err), pub, false, err
	case !cascade.HasKind(err, cascade.KindNotFound):
		return "", nil, false, err
	}
	wrote, created, err := createDaemonKey(ctx, b, name, rnd)
	if err != nil {
		return "", nil, false, err
	}
	// Re-read: what is returned is what custody holds, never what was meant.
	value, err = daemonKeyGet(ctx, b, name)
	if err != nil {
		return "", nil, false, err
	}
	pub, err = daemonPublicKey(name, value)
	if err == nil && created && !bytes.Equal(pub, wrote) {
		return "", nil, false, cascade.Newf(cascade.KindIntegrity,
			"secrets: the %s this process created is not the one custody holds after the write", name)
	}
	return keyIDFor(pub, err), pub, created && err == nil, err
}

// daemonKeyGet is the one non-elevated read of a reserved daemon key: the
// same contract as internalGet (a copy; KindNotFound when absent), which
// itself refuses these names so no rehydration or egress path reaches them.
func daemonKeyGet(ctx context.Context, b *Broker, name string) ([]byte, error) {
	if b == nil || b.custody == nil {
		return nil, ErrNoCustodyAvailable()
	}
	if !reservedDaemonKey(name) {
		return nil, cascade.Newf(cascade.KindInvalidInput, "secrets: %q is not a daemon key name", name)
	}
	value, err := b.custody.Get(ctx, name)
	if err != nil {
		if cascade.HasKind(err, cascade.KindNotFound) {
			return nil, cascade.Wrapf(cascade.KindNotFound, ErrVaultKeyNotFound, "secrets: reading <%s>: %v", name, err)
		}
		return nil, err
	}
	if value == nil {
		return nil, cascade.Wrapf(cascade.KindNotFound, ErrVaultKeyNotFound, "secrets: reading <%s>", name)
	}
	return append([]byte(nil), value...), nil
}

// createDaemonKey generates a key and writes it only if the name is still
// absent, returning the public half it wrote and whether it wrote. A custody
// with an atomic create-if-absent (the file vault) checks and writes in one
// locked step; any other is checked and written under the provisioning lock.
// It never overwrites.
func createDaemonKey(ctx context.Context, b *Broker, name string, rnd io.Reader) (ed25519.PublicKey, bool, error) {
	if rnd == nil {
		return nil, false, cascade.New(cascade.KindInvalidInput, "secrets: provisioning a daemon key needs a randomness source")
	}
	exists, err := b.Exists(ctx, name)
	if err != nil || exists {
		return nil, false, err
	}
	provisionYield()
	public, private, err := ed25519.GenerateKey(rnd)
	if err != nil {
		return nil, false, cascade.Wrap(cascade.KindInternal, err, "secrets: generating a daemon key")
	}
	defer zeroAll([][]byte{private})
	if c, ok := b.custody.(interface {
		createIfAbsent(context.Context, string, []byte) (bool, error)
	}); ok {
		created, err := c.createIfAbsent(ctx, name, private)
		return public, created, err
	}
	return public, true, b.custody.Set(ctx, name, private)
}

// daemonPublicKey checks the stored value's shape and derives its public
// half. The value is zeroed on every path. A value that is not exactly an
// Ed25519 private key, or whose embedded public half does not match its
// seed, is KindIntegrity: signing with it would mint records nothing verifies.
func daemonPublicKey(name string, value []byte) (ed25519.PublicKey, error) {
	defer zeroAll([][]byte{value})
	if len(value) != ed25519.PrivateKeySize {
		return nil, cascade.Newf(cascade.KindIntegrity,
			"secrets: the stored %s is %d bytes; an ed25519 private key is %d",
			name, len(value), ed25519.PrivateKeySize)
	}
	derived := ed25519.NewKeyFromSeed(value[:ed25519.SeedSize])
	defer zeroAll([][]byte{derived})
	if subtle.ConstantTimeCompare(derived, value) != 1 {
		return nil, cascade.Newf(cascade.KindIntegrity,
			"secrets: the stored %s does not hold a consistent ed25519 key pair", name)
	}
	return append(ed25519.PublicKey(nil), derived[ed25519.SeedSize:]...), nil
}

// keyIDFor is approvalKeyID when the key was readable, else empty.
func keyIDFor(pub ed25519.PublicKey, err error) string {
	if err != nil {
		return ""
	}
	return approvalKeyID(pub)
}
