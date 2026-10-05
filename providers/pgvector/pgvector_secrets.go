//go:build postgres

// Purpose: the Driver's DSN secret set, held behind a pointer whose type
// formats as a placeholder so no fmt verb on a Driver can print it.

package pgvector

// redacted is what every fmt verb prints in place of the secret set.
const redacted = "[redacted]"

// secretSet holds the plaintext spellings of the opened DSN's credentials.
// It sits behind a pointer with String and GoString so that formatting a
// Driver with any verb never prints the set. A nil *secretSet is the
// unknown set: callers then withhold driver text.
type secretSet struct{ forms []string }

// String keeps the set out of %v, %+v and %s.
func (secretSet) String() string { return redacted }

// GoString keeps the set out of %#v.
func (secretSet) GoString() string { return redacted }

// list returns the forms, or nil for the unknown (nil) set.
func (s *secretSet) list() []string {
	if s == nil {
		return nil
	}
	return s.forms
}

// newSecretSet wraps forms; nil forms (pgx could not parse the DSN) give
// the nil unknown set.
func newSecretSet(forms []string) *secretSet {
	if forms == nil {
		return nil
	}
	return &secretSet{forms: forms}
}
