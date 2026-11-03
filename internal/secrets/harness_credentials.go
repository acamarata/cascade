// Purpose: read and durably write one harness credential entry: new value
// staged in vault custody, previous kept 24 h in a rollback slot; a failed
// write leaves the entry byte-identical; only not-found is absence. Stores
// are portable (per-OS files pick one). SPORT: HarnessCredentials/ADDED.

package secrets

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// RollbackWindow is how long the previous entry stays in its rollback slot.
const RollbackWindow = 24 * time.Hour

// Entry is one harness credential document. Every formatting path prints
// only its length, never a token.
type Entry struct{ data []byte }

// NewEntry copies data into an Entry; a non-JSON document is KindInvalidInput.
func NewEntry(data []byte) (Entry, error) {
	if !json.Valid(data) {
		return Entry{}, cascade.New(cascade.KindInvalidInput, "secrets: a harness credential entry must be a JSON document")
	}
	return Entry{data: bytes.Clone(data)}, nil
}

// Bytes returns a copy of the entry document.
func (e Entry) Bytes() []byte { return bytes.Clone(e.data) }

// String prints the entry length only.
func (e Entry) String() string { return fmt.Sprintf("secrets.Entry{bytes:%d}", len(e.data)) }

// GoString prints the entry length only.
func (e Entry) GoString() string { return e.String() }

// MarshalJSON emits the entry length only.
func (e Entry) MarshalJSON() ([]byte, error) {
	return fmt.Appendf(nil, `{"redacted":true,"bytes":%d}`, len(e.data)), nil
}

// HarnessCredentials reads and durably writes one account's credential
// entry, keyed by the account's harness config directory.
type HarnessCredentials interface {
	// Read returns the live entry; ErrSecretNotFound only when it is absent.
	Read(ctx context.Context, configDir string) (Entry, error)
	// WriteDurable replaces the live entry, keeping the previous one in a
	// rollback slot; on any failure the live entry is byte-identical.
	WriteDurable(ctx context.Context, configDir string, e Entry) error
}

// entryStore is one OS's live entry; read is KindNotFound only when missing.
type entryStore interface {
	read(ctx context.Context, configDir string) ([]byte, error)
	write(ctx context.Context, configDir string, data []byte) error
}

var errHarnessCredentialsUnsupported = cascade.New(cascade.KindUnsupported,
	"secrets: harness credential entries are not supported on this platform")

// unsupportedStore refuses every call (windows).
type unsupportedStore struct{}

func (unsupportedStore) read(context.Context, string) ([]byte, error) {
	return nil, errHarnessCredentialsUnsupported
}
func (unsupportedStore) write(context.Context, string, []byte) error {
	return errHarnessCredentialsUnsupported
}

// harnessCredentials is the portable HarnessCredentials over one store.
type harnessCredentials struct {
	store   entryStore
	custody Custody
	clock   runtime.Clock
}

// Read implements HarnessCredentials; an unparseable entry is KindIntegrity.
func (h *harnessCredentials) Read(ctx context.Context, configDir string) (Entry, error) {
	data, err := h.store.read(ctx, configDir)
	if err == nil && !json.Valid(data) {
		err = cascade.New(cascade.KindIntegrity, "secrets: the harness credential entry is not a JSON document")
	}
	if err != nil {
		return Entry{}, err
	}
	return Entry{data: data}, nil
}

// WriteDurable implements HarnessCredentials: keep rollback, stage, write,
// read back by hash, and rewrite the previous entry on a mismatch.
func (h *harnessCredentials) WriteDurable(ctx context.Context, configDir string, e Entry) error {
	if !json.Valid(e.data) {
		return cascade.New(cascade.KindInvalidInput, "secrets: refusing to write an entry that is not a JSON document")
	}
	prev, err := h.store.read(ctx, configDir)
	hadPrev := err == nil
	if kind, _ := cascade.KindOf(err); err != nil && kind != cascade.KindNotFound {
		return err
	}
	staged, rollback := slotNames(configDir)
	if hadPrev {
		expires := binary.BigEndian.AppendUint64(nil, uint64(h.clock.Now().Add(RollbackWindow).Unix()))
		if err := h.custody.Set(ctx, rollback, append(expires, prev...)); err != nil {
			return err
		}
	}
	if err := h.custody.Set(ctx, staged, e.data); err != nil {
		return err
	}
	if err := h.store.write(ctx, configDir, e.data); err != nil {
		_ = h.custody.Delete(ctx, staged)
		return err
	}
	if got, err := h.store.read(ctx, configDir); err != nil || sha256.Sum256(got) != sha256.Sum256(e.data) {
		if hadPrev {
			_ = h.store.write(ctx, configDir, prev)
		}
		return cascade.New(cascade.KindIntegrity, "secrets: the harness credential entry did not read back as written")
	}
	_ = h.custody.Delete(ctx, staged)
	return nil
}

// slotNames names one config dir's custody staging and rollback slots.
func slotNames(configDir string) (staged, rollback string) {
	sum := sha256.Sum256([]byte(filepath.Clean(configDir)))
	key := "harness-cred." + hex.EncodeToString(sum[:8])
	return key + ".staged", key + ".rollback"
}

// fileStore is the linux entry <dir>/.credentials.json: 0600, owned by the
// current uid, replaced by an O_EXCL 0600 temp file renamed over it.
// writeFn and rename are test seams.
type fileStore struct {
	uid     int
	owner   func(os.FileInfo) (int, bool)
	writeFn func(f *os.File, b []byte) (int, error)
	rename  func(oldpath, newpath string) error
}

func errFileIO(op string) error {
	return cascade.Newf(cascade.KindUnavailable, "secrets: harness credentials %s failed", op)
}

// check refuses all but a regular file, mode 0600 or narrower, of our uid.
func (f fileStore) check(fi os.FileInfo) error {
	uid, ok := f.owner(fi)
	if !fi.Mode().IsRegular() || fi.Mode().Perm()&^0o600 != 0 || !ok || uid != f.uid {
		return cascade.New(cascade.KindPermissionDenied,
			"secrets: the harness credentials file is not a regular 0600 file owned by the current user; refused unchanged")
	}
	return nil
}

func (f fileStore) stat(path string) (os.FileInfo, error) {
	fi, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, errFileIO("stat")
	}
	return fi, f.check(fi)
}

func (f fileStore) read(_ context.Context, dir string) ([]byte, error) {
	path := filepath.Join(dir, ".credentials.json")
	if fi, err := f.stat(path); err != nil {
		return nil, err
	} else if fi == nil {
		return nil, ErrSecretNotFound(".credentials.json")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, errFileIO("read")
	}
	return data, nil
}

func (f fileStore) write(_ context.Context, dir string, data []byte) (err error) {
	path := filepath.Join(dir, ".credentials.json")
	if _, err := f.stat(path); err != nil {
		return err
	}
	fh, err := os.CreateTemp(dir, ".credentials.json.tmp-*") // O_EXCL, 0600
	if err != nil {
		return errFileIO("temp create")
	}
	defer func() {
		if err != nil {
			_ = fh.Close()
			_ = os.Remove(fh.Name())
		}
	}()
	if fi, statErr := fh.Stat(); statErr != nil || f.check(fi) != nil {
		return cascade.New(cascade.KindPermissionDenied, "secrets: the harness credentials temp file did not verify as 0600")
	}
	if n, werr := f.writeFn(fh, data); werr != nil || n != len(data) || errors.Join(fh.Sync(), fh.Close()) != nil {
		return errFileIO("write")
	}
	if f.rename(fh.Name(), path) != nil {
		return errFileIO("rename")
	}
	_, err = f.stat(path) // stat-verify 0600 and the current uid after the rename
	return err
}

// keychainStore is the darwin entry: a generic password (account = OS user)
// whose service names the config dir's literal (symlink) path, falling back
// to its realpath. basis records, per dir, which path and service answered.
type keychainStore struct {
	run      commandRunner
	ident    func(context.Context) (user, keychain string, err error)
	realpath func(string) (string, error)
	mu       sync.Mutex
	basis    map[string][2]string
}

// vendorKeychainServicePrefix is the vendor harness's keychain item prefix (a vendor interface, defined once).
const vendorKeychainServicePrefix = "Claude Code-credentials-"

func harnessServiceName(path string) string {
	sum := sha256.Sum256([]byte(path))
	return vendorKeychainServicePrefix + hex.EncodeToString(sum[:4])
}

// services lists the literal-path service, then the realpath one if it differs.
func (k *keychainStore) services(dir string) [][2]string {
	lit := filepath.Clean(dir)
	out := [][2]string{{"literal", harnessServiceName(lit)}}
	if resolved, err := k.realpath(lit); err == nil && resolved != lit {
		out = append(out, [2]string{"realpath", harnessServiceName(resolved)})
	}
	return out
}

func (k *keychainStore) security(ctx context.Context, op string, args ...string) ([]byte, bool, error) {
	out, err := k.run(ctx, "/usr/bin/security", args...)
	if err != nil && isKeychainNotFound(err) {
		return nil, false, nil
	} else if err != nil {
		return nil, false, cascade.Newf(cascade.KindUnavailable, "secrets: harness keychain %s failed (diagnostics withheld)", op)
	}
	return out, true, nil
}

func (k *keychainStore) read(ctx context.Context, dir string) ([]byte, error) {
	user, _, err := k.ident(ctx)
	if err != nil {
		return nil, err
	}
	for _, svc := range k.services(dir) {
		out, found, err := k.security(ctx, "read", "find-generic-password", "-a", user, "-s", svc[1], "-w")
		if err != nil {
			return nil, err
		} else if !found {
			continue
		}
		k.mu.Lock()
		k.basis[filepath.Clean(dir)] = svc
		k.mu.Unlock()
		text := bytes.TrimRight(out, "\n")
		if decoded, err := hex.DecodeString(string(text)); err == nil && !json.Valid(text) {
			return decoded, nil
		}
		return text, nil
	}
	return nil, ErrSecretNotFound("harness keychain entry")
}

// write replaces (-U) the entry under the recorded service (literal when
// none) in the default keychain, as hex (-X) so plaintext never enters argv.
func (k *keychainStore) write(ctx context.Context, dir string, data []byte) error {
	user, kc, err := k.ident(ctx)
	if err != nil {
		return err
	}
	k.mu.Lock()
	svc, ok := k.basis[filepath.Clean(dir)]
	k.mu.Unlock()
	if !ok {
		svc = k.services(dir)[0]
	}
	_, _, err = k.security(ctx, "write", "add-generic-password", "-U", "-a", user, "-s", svc[1], "-X", hex.EncodeToString(data), kc)
	return err
}
