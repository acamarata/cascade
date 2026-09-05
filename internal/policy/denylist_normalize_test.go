// Purpose: R-21.212's obfuscation table. One deny-list row names one
// operation; every row below expresses THAT SAME operation through a
// different outer form and asserts it still matches. This is the file that
// would have caught a matcher that only ever sees the string a caller
// typed.
//
// The refusal half is equally load-bearing: a form the normalizer cannot
// resolve is an ERROR, and layer 1 reads a deny-lister error as a match,
// so nothing in here can fall through to an allow.
//
// SPORT: internal/policy denylist-normalizer/ADDED (P1-E09-W2-S17-T4).
package policy

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestDenyListMatchesNormalizedForm is R-21.212. The single configured row
// is `rm -rf /srv`; every command below runs exactly that.
func TestDenyListMatchesNormalizedForm(t *testing.T) {
	ctx := context.Background()
	f := newDenyFixture(t)
	if err := f.engine.Add(ctx, "rm -rf /srv", anyActionClass); err != nil {
		t.Fatalf("Add: %v", err)
	}
	cases := []struct {
		name   string
		action string
	}{
		{"the bare form", "rm -rf /srv"},
		{"single quoting", `'rm' -rf /srv`},
		{"double quoting", `"rm" "-rf" "/srv"`},
		{"quoting inside a word", `r"m" -rf /srv`},
		{"backslash escaping", `\rm -rf /srv`},
		{"escaping mid-word", `r\m -rf /srv`},
		{"an absolute path", "/bin/rm -rf /srv"},
		{"an environment-assignment prefix", "LC_ALL=C rm -rf /srv"},
		{"a nested shell", `sh -c 'rm -rf /srv'`},
		{"a doubly nested shell", `bash -c "sh -c 'rm -rf /srv'"`},
		{"a command substitution", "echo $(rm -rf /srv)"},
		{"a chained and", "ls && rm -rf /srv"},
		{"a chained or", "ls || rm -rf /srv"},
		{"a pipeline", "ls | rm -rf /srv"},
		{"a subshell", "(rm -rf /srv)"},
		{"a block", "{ rm -rf /srv; }"},
		{"a remote command", "ssh host rm -rf /srv"},
		{"an xargs indirection", "xargs -n 1 rm -rf /srv"},
		{"a nested shell reached through a path", `/bin/sh -c 'rm -rf /srv'`},
	}
	for _, tc := range cases {
		denied, err := f.engine.Denied(ctx, tc.action)
		if err != nil {
			t.Errorf("%s: Denied(%q): %v", tc.name, tc.action, err)
			continue
		}
		if !denied {
			t.Errorf("%s: %q evaded the deny-list entry", tc.name, tc.action)
		}
	}
}

// TestNormalizationRefusesWhatItCannotResolve is hard requirement 2's
// third direction: an input the matcher cannot normalise DENIES. Layer 1
// reads the error as a match, so none of these can fall through.
func TestNormalizationRefusesWhatItCannotResolve(t *testing.T) {
	ctx := context.Background()
	f := newDenyFixture(t)
	if err := f.engine.Add(ctx, "rm -rf /srv", anyActionClass); err != nil {
		t.Fatalf("Add: %v", err)
	}
	cases := []struct {
		name   string
		action string
	}{
		{"empty", "   "},
		{"unparseable", "rm -rf '"},
		{"an unmodeled form", "for i in 1 2; do rm -rf /srv; done"},
		{"a command name that is not a literal", "$CMD -rf /srv"},
		{"a C-style quoted name", `$'\x72\x6d' -rf /srv`},
		{"an unresolvable shell argument", "sh -c $SCRIPT"},
		{"an unrecognised xargs option", "xargs --zzz rm -rf /srv"},
	}
	for _, tc := range cases {
		denied, err := f.engine.Denied(ctx, tc.action)
		if err == nil {
			t.Errorf("%s: %q normalized silently (denied=%v); it must refuse",
				tc.name, tc.action, denied)
		}
		if denied {
			t.Errorf("%s: a refusal also reported a match", tc.name)
		}
	}
}

// TestPartialFormsStillMatch asserts the fail-closed direction for a
// command whose ARGUMENTS cannot be read. `rm -rf $TARGET` is a real,
// common shape; it must not be a way past a `rm -rf *` entry.
func TestPartialFormsStillMatch(t *testing.T) {
	ctx := context.Background()
	f := newDenyFixture(t)
	if err := f.engine.Add(ctx, "rm -rf /srv/*", anyActionClass); err != nil {
		t.Fatalf("Add: %v", err)
	}
	for _, action := range []string{"rm -rf $TARGET", "rm -rf /srv/$NAME", `rm -rf "$@"`} {
		denied, err := f.engine.Denied(ctx, action)
		if err != nil {
			t.Errorf("Denied(%q): %v", action, err)
			continue
		}
		if !denied {
			t.Errorf("%q evaded the entry through an unreadable argument", action)
		}
	}
	// The fail-closed rule must not become a match-all: a different
	// command with an unreadable argument is still not this entry.
	if denied, err := f.engine.Denied(ctx, "ls $TARGET"); err != nil || denied {
		t.Errorf("Denied(\"ls $TARGET\") = %v, %v; want no match", denied, err)
	}
}

// TestNormalizationBoundsWrapperRecursion asserts a command cannot buy
// unbounded work, and that hitting the bound refuses rather than returning
// the forms found so far.
func TestNormalizationBoundsWrapperRecursion(t *testing.T) {
	ctx := context.Background()
	// Three real levels of nesting still normalize.
	nested := `sh -c "bash -c \"zsh -c 'rm -rf /srv'\""`
	forms, err := collectForms(ctx, nested, 0)
	if err != nil {
		t.Fatalf("a three-deep wrapper refused: %v", err)
	}
	if !strings.Contains(renderForms(forms), "rm -rf /srv") {
		t.Fatalf("the inner command was not reached: %v", forms)
	}
	// At the bound, normalization refuses rather than returning the forms
	// it happens to have found.
	if _, err := collectForms(ctx, "rm -rf /srv", maxWrapperDepth); err == nil {
		t.Fatal("normalization ran past the wrapper-recursion bound")
	}
}

// renderForms joins the collected forms for a failure message.
func renderForms(forms []denyForm) string {
	texts := make([]string, 0, len(forms))
	for _, f := range forms {
		texts = append(texts, f.text)
	}
	return strings.Join(texts, " | ")
}

// TestNormalizationHonoursCancellation asserts a canceled context refuses
// rather than returning a partial answer.
func TestNormalizationHonoursCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := normalizedForms(ctx, "rm -rf /srv"); err == nil {
		t.Fatal("normalization ran after cancellation")
	}
	f := newDenyFixture(t)
	if _, err := f.engine.Denied(ctx, "rm -rf /srv"); err == nil {
		t.Fatal("Denied answered after cancellation")
	}
}

// TestNormalizedFormsIncludeTheOriginal asserts the trimmed original is
// always a form, so an operator who pastes the exact string an audit row
// showed them writes an entry that matches.
func TestNormalizedFormsIncludeTheOriginal(t *testing.T) {
	forms, err := normalizedForms(context.Background(), "  LC_ALL=C /bin/rm -rf /srv  ")
	if err != nil {
		t.Fatalf("normalizedForms: %v", err)
	}
	want := map[string]bool{
		"LC_ALL=C /bin/rm -rf /srv": false,
		"rm -rf /srv":               false,
		"/bin/rm -rf /srv":          false,
	}
	for _, f := range forms {
		if _, ok := want[f.text]; ok {
			want[f.text] = true
		}
	}
	for text, seen := range want {
		if !seen {
			t.Errorf("the form %q was not produced; got %v", text, forms)
		}
	}
}

// TestNormalizationRefusalsCarryAClassifierCode asserts the refusals reuse
// the classifier's stable codes rather than inventing a third vocabulary.
func TestNormalizationRefusalsCarryAClassifierCode(t *testing.T) {
	_, err := normalizedForms(context.Background(), "rm -rf '")
	if !errors.Is(err, ErrClassifyParseError) {
		t.Errorf("an unparseable command returned %v, want %s", err, CodeClassifyParseError)
	}
	_, err = normalizedForms(context.Background(), "$CMD")
	if !errors.Is(err, ErrClassifyUnknown) {
		t.Errorf("an unresolvable name returned %v, want %s", err, CodeClassifyUnknown)
	}
}

// TestWrapperInnerFormsAreNormalized covers the wrapper descent that
// xargs and ssh take, in both directions: the inner command is reached,
// and an inner that cannot be read refuses.
func TestWrapperInnerFormsAreNormalized(t *testing.T) {
	ctx := context.Background()
	f := newDenyFixture(t)
	if err := f.engine.Add(ctx, "rm -rf /srv", anyActionClass); err != nil {
		t.Fatalf("Add: %v", err)
	}
	matches := []struct{ name, action string }{
		{"a wrapper inside a wrapper", `xargs -n 1 sh -c 'rm -rf /srv'`},
		{"a remote wrapper inside a wrapper", `ssh -p 22 host xargs rm -rf /srv`},
		{"an inner reached through a path", "xargs /bin/rm -rf /srv"},
	}
	for _, tc := range matches {
		denied, err := f.engine.Denied(ctx, tc.action)
		if err != nil || !denied {
			t.Errorf("%s: Denied(%q) = %v, %v; want a match", tc.name, tc.action, denied, err)
		}
	}
	refusals := []struct{ name, action string }{
		{"an inner name that is not a literal", "xargs -n 1 $CMD -rf /srv"},
		{"an unreadable ssh destination", "ssh $HOST rm -rf /srv"},
		{"an unrecognised ssh option", "ssh --zzz host rm -rf /srv"},
	}
	for _, tc := range refusals {
		if _, err := f.engine.Denied(ctx, tc.action); err == nil {
			t.Errorf("%s: %q normalized silently", tc.name, tc.action)
		}
	}
	// A wrapper with no inner command at all is not an error: there is
	// simply nothing further to normalize, and the classifier already
	// puts the form at the top rung.
	for _, action := range []string{"ssh host", "xargs", "sh script.sh"} {
		if _, err := normalizedForms(ctx, action); err != nil {
			t.Errorf("normalizedForms(%q) refused a wrapper with no inner: %v", action, err)
		}
	}
}

// TestNormalizedFormsAreDeduplicated asserts a repeated invocation does
// not grow the form list, so a long chained command cannot buy quadratic
// matching work.
func TestNormalizedFormsAreDeduplicated(t *testing.T) {
	forms, err := normalizedForms(context.Background(), "rm -rf /srv && rm -rf /srv && rm -rf /srv")
	if err != nil {
		t.Fatalf("normalizedForms: %v", err)
	}
	seen := map[string]int{}
	for _, f := range forms {
		seen[f.text]++
	}
	for text, count := range seen {
		if count > 1 {
			t.Errorf("the form %q was emitted %d times", text, count)
		}
	}
}
