// Purpose: the deny-list grammar and matcher, asserted directly. The
// pattern rules and the glob semantics are the load-bearing half of layer
// 1's configurable portion: a matcher that under-matches fails OPEN, so
// every row below states the evasion it closes rather than restating the
// implementation.
//
// SPORT: internal/policy DenyListEngine/ADDED, DefaultDenyList/ADDED
// (P1-E09-W2-S17-T4).
package policy

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestValidateDenyPatternRefusesWhatTheMatcherCannotHonour covers the
// grammar in both directions. Each refusal row names the reason the
// pattern is refused at WRITE time rather than silently mis-matched later.
func TestValidateDenyPatternRefusesWhatTheMatcherCannotHonour(t *testing.T) {
	cases := []struct {
		name    string
		pattern string
		wantErr bool
	}{
		{"a bare command", "rm -rf /", false},
		{"a trailing wildcard", "rm -rf *", false},
		{"a single-character wildcard", "r? -rf /", false},
		{"a leading wildcard", "*curl*", false},
		{"empty", "", true},
		{"whitespace only", "   ", true},
		{"padded", " rm -rf / ", true},
		{"a character class", "rm -rf /[ab]", true},
		{"an unbalanced bracket", "rm [", true},
		{"an escape", `rm \-rf /`, true},
		{"a control character", "rm\x00-rf", true},
	}
	for _, tc := range cases {
		err := validateDenyPattern(tc.pattern)
		if (err != nil) != tc.wantErr {
			t.Errorf("%s: validateDenyPattern(%q) = %v, wantErr %v",
				tc.name, tc.pattern, err, tc.wantErr)
			continue
		}
		if err != nil && !errors.Is(err, ErrDenyListPatternInvalid) {
			t.Errorf("%s: refusal %v does not carry %s", tc.name, err,
				CodeDenyListPatternInvalid)
		}
	}
}

// TestGlobMatchHasNoPathSeparatorSemantics is the reason filepath.Match is
// not used. Under filepath.Match a `*` cannot cross a `/`, so `rm *` would
// NOT match `rm -rf /home/user` and the deny-list entry an operator wrote
// would fail open. Each row here asserts the direction that matters.
func TestGlobMatchHasNoPathSeparatorSemantics(t *testing.T) {
	cases := []struct {
		name    string
		pattern string
		subject string
		want    bool
	}{
		{"exact", "rm -rf /", "rm -rf /", true},
		{"a wildcard crosses a separator", "rm *", "rm -rf /home/user", true},
		{"a wildcard crosses several", "*secrets*", "cat /etc/a/b/secrets.env", true},
		{"question matches one byte", "r? -rf", "rm -rf", true},
		{"question does not match two", "r? -rf", "rmx -rf", false},
		{"a longer subject does not match a shorter exact", "rm", "rm -rf /", false},
		{"a different command does not match", "rm *", "ls -la", false},
		{"consecutive wildcards collapse", "rm**/", "rm -rf /", true},
		{"a trailing wildcard matches the empty tail", "rm -rf /*", "rm -rf /", true},
	}
	for _, tc := range cases {
		if got := globMatch(tc.pattern, tc.subject, false); got != tc.want {
			t.Errorf("%s: globMatch(%q, %q) = %v, want %v",
				tc.name, tc.pattern, tc.subject, got, tc.want)
		}
	}
}

// TestGlobMatchOnAPartialFormFailsClosed asserts the rule that keeps an
// unreadable argument from being an evasion: when the normalizer could not
// read a command's tail, any pattern that tail COULD have completed counts
// as a match. `rm $TARGET` must not slip past a `rm -rf *` entry.
func TestGlobMatchOnAPartialFormFailsClosed(t *testing.T) {
	cases := []struct {
		name             string
		pattern, subject string
		wantWhole        bool
		wantPartial      bool
	}{
		{"an unread tail could complete the pattern", "rm -rf *", "rm", false, true},
		{"an unread tail could supply literal text", "rm -rf /", "rm ", false, true},
		{"an unread tail cannot rewrite what was read", "ls *", "rm", false, false},
		{"a whole match is a match either way", "rm *", "rm -rf", true, true},
	}
	for _, tc := range cases {
		if got := globMatch(tc.pattern, tc.subject, false); got != tc.wantWhole {
			t.Errorf("%s: whole match = %v, want %v", tc.name, got, tc.wantWhole)
		}
		if got := globMatch(tc.pattern, tc.subject, true); got != tc.wantPartial {
			t.Errorf("%s: partial match = %v, want %v", tc.name, got, tc.wantPartial)
		}
	}
}

// TestDenyRuleValidateRejectsAnUnknownClass asserts the wildcard class is
// the ZERO value and every other out-of-range class is refused. The zero
// value must NOT run through safeActionClass, which would silently narrow
// an "always" row to destructive_privileged.
func TestDenyRuleValidateRejectsAnUnknownClass(t *testing.T) {
	if err := (DenyRule{Pattern: "rm *", Class: anyActionClass}).Validate(); err != nil {
		t.Errorf("the wildcard class was refused: %v", err)
	}
	for _, class := range []ActionClass{ClassRead, ClassDestructivePrivileged} {
		if err := (DenyRule{Pattern: "rm *", Class: class}).Validate(); err != nil {
			t.Errorf("class %s was refused: %v", class, err)
		}
	}
	for _, bad := range []ActionClass{6, 200} {
		err := (DenyRule{Pattern: "rm *", Class: bad}).Validate()
		if !errors.Is(err, ErrDenyListPatternInvalid) {
			t.Errorf("class %d was accepted (%v)", bad, err)
		}
	}
	if !(DenyRule{Class: anyActionClass}).scopedTo(ClassRead) {
		t.Error("a wildcard row must answer for every class")
	}
	if (DenyRule{Class: ClassRead}).scopedTo(ClassDestructivePrivileged) {
		t.Error("a scoped row answered for another class")
	}
}

// TestDefaultDenyListIsACompleteBehaviour asserts DefaultDenyList is a
// real named default and not a placeholder (Art.1): it answers every
// query, and it REFUSES a write rather than accepting one it cannot keep.
func TestDefaultDenyListIsACompleteBehaviour(t *testing.T) {
	ctx := context.Background()
	d := DefaultDenyList()
	for _, action := range []string{"rm -rf /", "", "sh -c 'rm -rf /'"} {
		denied, err := d.Denied(ctx, action)
		if denied || err != nil {
			t.Errorf("Denied(%q) = %v, %v; want false, nil", action, denied, err)
		}
	}
	got, err := d.ContainsClass(ctx, ClassDestructivePrivileged, "rm -rf /")
	if got || err != nil {
		t.Errorf("ContainsClass = %v, %v; want false, nil", got, err)
	}
	list, err := d.List(ctx)
	if len(list) != 0 || err != nil {
		t.Errorf("List = %v, %v; want empty, nil", list, err)
	}
	if err := d.Remove(ctx, "rm *"); err != nil {
		t.Errorf("Remove on an empty list returned %v", err)
	}
	err = d.Add(ctx, "rm *", anyActionClass)
	if !errors.Is(err, ErrDenyListStoreError) {
		t.Errorf("Add = %v, want a %s refusal", err, CodeDenyListStoreError)
	}
	if !strings.Contains(err.Error(), CodeDenyListStoreError) {
		t.Errorf("the refusal %q does not name its code", err)
	}
}

// TestNewStoreDenyListRequiresAStore asserts the constructor refuses an
// engine that would have to assume the list is empty.
func TestNewStoreDenyListRequiresAStore(t *testing.T) {
	if _, err := NewStoreDenyList(nil); err == nil {
		t.Fatal("NewStoreDenyList(nil) built an engine with nowhere to read from")
	}
}
