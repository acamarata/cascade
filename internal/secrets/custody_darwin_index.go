//go:build darwin

// Purpose: the name INDEX the darwin backend keeps beside its entries, so
//
//	List can enumerate one service's secrets without `dump-keychain` --
//	which prompts once per stored item and can be asked to print secret
//	data. Split from custody_darwin.go for the 300-line cap.
//
// Constraints: every /usr/bin/security invocation here carries the
//
//	explicitly resolved keychain as its trailing argument, like every
//	other call in this package (R-14.260). The index was its own route to
//	the default search list, and the search list is what makes a GUI
//	dialog possible.
//
// SPORT: internal/secrets Custody/ADDED (darwin keychain name index).

package secrets

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
)

// indexAccount is the account attribute of the name-index entry. It is
// namespaced away from the secret accounts so it can never collide with a
// stored name (validateSecretName rejects ':').
const indexAccount = keychainAccountPrefix + "index:names"

// readIndex loads the stored name list. A missing index is an empty vault;
// an index that will not decode is an integrity refusal, never a silently
// empty list, because reporting "no secrets" for a vault that has them
// invites a caller to overwrite them.
func (k *keychainCustody) readIndex(ctx context.Context) ([]string, error) {
	keychain, err := k.resolveKeychain(ctx)
	if err != nil {
		return nil, err
	}
	out, err := k.run(ctx, securityBin, "find-generic-password",
		"-a", indexAccount, "-s", k.service, "-w", keychain)
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
	keychain, err := k.resolveKeychain(ctx)
	if err != nil {
		return err
	}
	if _, err := k.run(ctx, securityBin, "add-generic-password",
		"-a", indexAccount, "-s", k.service, "-U", "-X", hex.EncodeToString(encoded), keychain); err != nil {
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
