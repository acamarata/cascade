package egress

import (
	"context"
	"strings"
)

// Vault is the value source the exact-substring pass reads. It is an
// interface rather than a concrete vault so this package never imports a
// custody backend, and so a caller can supply a reader that does not
// prompt for elevation on the egress path.
//
// *secrets.Broker satisfies it as written.
type Vault interface {
	// List returns every stored name.
	List(ctx context.Context) ([]string, error)
	// Get returns one stored value.
	Get(ctx context.Context, name string) ([]byte, error)
}

// minExactValueBytes is the shortest stored value the exact-substring
// pass will match.
//
// This is a LENGTH floor, not an entropy floor: the two-pass rule says
// the exact pass applies no entropy and no confidence judgement, and it
// does not. The floor exists because a stored value of one or two bytes
// occurs constantly in ordinary text, and substituting every "a" in a
// payload destroys the payload while protecting nothing. STATED GAP: a
// stored value shorter than this is not caught by the exact pass. It is
// still caught by the detector pass when it has credential shape, and not
// caught at all when it does not.
const minExactValueBytes = 8

// vaultValue is one stored secret's name and bytes, held only for the
// duration of one Intercept call.
type vaultValue struct {
	name  string
	value []byte
}

// loadVaultValues reads every stored value, dropping the ones below the
// length floor. Order is longest value first, then name ascending, so
// that a value containing another value is substituted first and the
// result does not depend on the vault's listing order.
func loadVaultValues(ctx context.Context, vault Vault) ([]vaultValue, error) {
	names, err := vault.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]vaultValue, 0, len(names))
	for _, name := range names {
		value, gerr := vault.Get(ctx, name)
		if gerr != nil {
			return nil, gerr
		}
		if len(value) < minExactValueBytes {
			continue
		}
		out = append(out, vaultValue{name: name, value: value})
	}
	sortVaultValues(out)
	return out, nil
}

// vaultRefName normalises a stored name into the UPPER_SNAKE reference
// the tag grammar accepts. It carries no information about the value: two
// different secrets stored under the same name produce the same
// reference, which is what keeps the placeholder from being an oracle.
func vaultRefName(name string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(name) {
		switch {
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := b.String()
	if out == "" || out[0] < 'A' || out[0] > 'Z' {
		return "V_" + out
	}
	return out
}
