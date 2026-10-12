package egress

import (
	"context"
	"strings"
)

// auditOnlyInventory lists, in sorted order, the classes inventoried without
// an enforced transit path. AuditOnly and AuditOnlyClasses both read it, so
// the two views cannot drift.
var auditOnlyInventory = []struct {
	class  EgressClass
	reason string
}{
	{EgressClassOAuth, "Token endpoint requests do not yet transit the egress engine."},
	{EgressClassSpikeMeasurement, "Measurement requests do not yet transit the egress engine."},
	{EgressClassWikiGitPush, "The git push process does not yet transit the egress engine."},
}

// AuditOnly explains classes inventoried without an enforced transit path.
func AuditOnly(class EgressClass) (reason string, ok bool) {
	for _, entry := range auditOnlyInventory {
		if entry.class == class {
			return entry.reason, true
		}
	}
	return "", false
}

// AuditOnlyClasses returns a sorted, independent inventory for diagnostics.
func AuditOnlyClasses() []EgressClass {
	out := make([]EgressClass, 0, len(auditOnlyInventory))
	for _, entry := range auditOnlyInventory {
		out = append(out, entry.class)
	}
	return out
}

// StoredValueIn reports whether text holds any stored vault value of at least
// minBytes bytes. It is a plain substring test: no secret-reference tag is
// exempt and no shape or entropy judgement applies, so a caller that must
// refuse rather than rewrite (a fetch URL) can use a lower floor than the
// substitution pass. foldCase compares ASCII-case-insensitively, for text
// such as a host name that the network treats without case. The capability
// is checked first, exactly as Intercept checks it, so the method is no
// oracle for a holder without one. Values shorter than one byte never match.
func (e *Engine) StoredValueIn(ctx context.Context, token Capability, text string, minBytes int, foldCase bool) (bool, error) {
	if err := e.admit(token, TierPublic); err != nil {
		return false, err
	}
	names, err := e.vault.List(ctx)
	if err != nil {
		return false, err
	}
	if foldCase {
		text = strings.ToLower(text)
	}
	for _, name := range names {
		value, gerr := e.vault.Get(ctx, name)
		if gerr != nil {
			return false, gerr
		}
		if len(value) == 0 || len(value) < minBytes {
			continue
		}
		needle := string(value)
		if foldCase {
			needle = strings.ToLower(needle)
		}
		if strings.Contains(text, needle) {
			return true, nil
		}
	}
	return false, nil
}
