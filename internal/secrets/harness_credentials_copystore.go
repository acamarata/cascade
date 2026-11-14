// Purpose: each entry store's view of the account copy set: copyRefs names
// the copies (DaemonCopies for the store's platform), readCopy and writeCopy
// reach one copy. The keychain store goes through its one security wrapper
// and shares read's decode; the file store reuses read and write.
// SPORT: HarnessCredentials/CHANGED.

package secrets

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
)

// decodeKeychainEntry trims the tool's newline and decodes a hex-stored
// entry; a JSON document is returned as is.
func decodeKeychainEntry(out []byte) []byte {
	text := bytes.TrimRight(out, "\n")
	if decoded, err := hex.DecodeString(string(text)); err == nil && !json.Valid(text) {
		return decoded
	}
	return text
}

func (k *keychainStore) copyRefs(configDir string) ([]CopyRef, error) {
	defaultDir := ""
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		defaultDir = filepath.Join(home, ".claude")
	}
	return daemonCopiesFor("darwin", configDir, k.realpath, defaultDir)
}

func (k *keychainStore) readCopy(ctx context.Context, ref CopyRef) ([]byte, error) {
	user, _, err := k.ident(ctx)
	if err != nil {
		return nil, err
	}
	out, found, err := k.security(ctx, "read", "find-generic-password", "-a", user, "-s", ref.service, "-w")
	if err != nil {
		return nil, err
	} else if !found {
		return nil, ErrSecretNotFound("harness keychain entry")
	}
	return decodeKeychainEntry(out), nil
}

// writeCopy replaces (-U) one copy's item as hex (-X), so plaintext never
// enters argv.
func (k *keychainStore) writeCopy(ctx context.Context, ref CopyRef, data []byte) error {
	user, kc, err := k.ident(ctx)
	if err != nil {
		return err
	}
	_, _, err = k.security(ctx, "write", "add-generic-password", "-U", "-a", user, "-s", ref.service, "-X", hex.EncodeToString(data), kc)
	return err
}

func (f fileStore) copyRefs(configDir string) ([]CopyRef, error) {
	return daemonCopiesFor("linux", configDir, nil, "")
}

func (f fileStore) readCopy(ctx context.Context, ref CopyRef) ([]byte, error) {
	return f.read(ctx, filepath.Dir(ref.path))
}

func (f fileStore) writeCopy(ctx context.Context, ref CopyRef, data []byte) error {
	return f.write(ctx, filepath.Dir(ref.path), data)
}

func (unsupportedStore) copyRefs(string) ([]CopyRef, error) {
	return nil, errHarnessCredentialsUnsupported
}

func (unsupportedStore) readCopy(context.Context, CopyRef) ([]byte, error) {
	return nil, errHarnessCredentialsUnsupported
}

func (unsupportedStore) writeCopy(context.Context, CopyRef, []byte) error {
	return errHarnessCredentialsUnsupported
}
