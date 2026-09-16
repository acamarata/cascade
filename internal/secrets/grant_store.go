// Purpose: the on-disk grant register and the id derivation beside it.
//
// WHY GRANTS DO NOT LIVE IN CUSTODY (a recorded deviation from R-14.243's
//
//	own wording, which said "custody-backed"). A grant is an AUTHORIZATION
//	record, not a secret. Putting it in custody would place it under the
//	vault verbs — list, get, set, delete — so the same commands that manage
//	secrets would manage the authorization to read them, and `vault set`
//	would become a way to mint a grant with no elevation at all. It lives
//	in its own 0600 file beside the vault instead, written atomically.
//	R-14.243's eight binding properties are unchanged; only the storage
//	medium differs, and this comment is the record of why.
//
// Inputs: a directory.
// Outputs: LoadGrants/SaveGrants.
// Constraints: no value of any secret ever enters this file's data. A
//
//	missing register is an EMPTY one, never an error: a daemon that has
//	never been granted anything must fail closed at the credential
//	boundary with a message naming the key, not fail to start.
//
// SPORT: internal/secrets grant-store/ADD — P1-E10-W4-S87-T1.

package secrets

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// grantsFileName is the register's name inside the vault directory.
const grantsFileName = "grants.json"

// grantIDBytes is how much of the digest an id carries. Eight bytes is
// enough to name a handful of grants unambiguously and short enough to
// retype when revoking.
const grantIDBytes = 8

// grantID derives a stable, non-secret identifier for a grant.
//
// It digests the key REFERENCE and the issue time, never a value: this id
// is printed by `cascade vault grants` and written to every audit record.
func grantID(keyRef string, issuedAt time.Time) string {
	sum := sha256.Sum256([]byte(keyRef + "@" + issuedAt.UTC().Format(time.RFC3339Nano)))
	return hex.EncodeToString(sum[:grantIDBytes])
}

// FileGrantStore keeps the register in one JSON file under dir.
type FileGrantStore struct {
	dir string
}

// NewFileGrantStore builds a store rooted at dir.
func NewFileGrantStore(dir string) (*FileGrantStore, error) {
	if dir == "" {
		return nil, cascade.New(cascade.KindInvalidInput,
			"secrets: a grant store needs a directory")
	}
	return &FileGrantStore{dir: dir}, nil
}

// path is the register file.
func (s *FileGrantStore) path() string { return filepath.Join(s.dir, grantsFileName) }

// LoadGrants reads the register. A missing file is an empty register.
func (s *FileGrantStore) LoadGrants(context.Context) ([]Grant, error) {
	raw, err := os.ReadFile(s.path())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "secrets: reading the grant register")
	}
	var grants []Grant
	if err := json.Unmarshal(raw, &grants); err != nil {
		// A corrupt register is NOT treated as empty: reading it as "no
		// grants" would silently revoke every grant the operator issued,
		// and reading it as "all grants" would be worse.
		return nil, cascade.Wrap(cascade.KindIntegrity, err, "secrets: the grant register is unreadable")
	}
	return grants, nil
}

// SaveGrants replaces the register atomically.
func (s *FileGrantStore) SaveGrants(_ context.Context, grants []Grant) error {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "secrets: creating the vault directory")
	}
	encoded, err := json.MarshalIndent(grants, "", "  ")
	if err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "secrets: encoding the grant register")
	}
	tmp, err := os.CreateTemp(s.dir, grantsFileName+".*")
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "secrets: creating the grant register")
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return cascade.Wrap(cascade.KindUnavailable, err, "secrets: securing the grant register")
	}
	if _, err := tmp.Write(append(encoded, '\n')); err != nil {
		_ = tmp.Close()
		return cascade.Wrap(cascade.KindUnavailable, err, "secrets: writing the grant register")
	}
	// fsync before the rename: a register that survives the rename but not
	// the write would authorise nothing, or worse, something stale.
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return cascade.Wrap(cascade.KindUnavailable, err, "secrets: flushing the grant register")
	}
	if err := tmp.Close(); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "secrets: closing the grant register")
	}
	if err := os.Rename(tmp.Name(), s.path()); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "secrets: replacing the grant register")
	}
	return nil
}
