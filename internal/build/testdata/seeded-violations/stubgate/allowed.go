// Package violation (allowed.go) proves the CASCADE-ALLOW escape: every
// marker below is preceded by a syntactically valid
// `// CASCADE-ALLOW: <ticket-id> <reason>` comment and must produce ZERO
// stub-gate findings.
package violation

// CASCADE-ALLOW: P1-E01-W1-S01-T8 known gap, tracked for follow-up
// TODO: escaped by the line above.

// CASCADE-ALLOW: P1-E01-W1-S01-T8 known gap, tracked for follow-up
// FIXME: escaped by the line above.

// CASCADE-ALLOW: P1-E01-W1-S01-T8 known gap, tracked for follow-up
// XXX: escaped by the line above.

// CASCADE-ALLOW: P1-E01-W1-S01-T8 this stays unimplemented until follow-up lands
func AllowedStub() {
	// CASCADE-ALLOW: P1-E01-W1-S01-T8 panic escaped for this fixture
	panic("not implemented")
}

func AllowedPlaceholder() (int, error) {
	// CASCADE-ALLOW: P1-E01-W1-S01-T8 placeholder escaped for this fixture
	return nil, nil // placeholder
}

// CASCADE-ALLOW: P1-E01-W1-S01-T8 mock type escaped for this fixture
type MockAllowed struct{}
