// Purpose (this file): unit coverage for runner.go's small pure helpers —
//
//	firstLine, containsFold, isPushConflict, newRunner's production
//	default, and defaultGetenv — that sync_test.go/drift_test.go's
//	end-to-end tests do not each independently exercise.
//
// SPORT: plugins/github/wiki:runner (ADD) — P1-E25-W5-S51-T6.
package wiki

import (
	"os"
	"testing"
)

func TestFirstLine(t *testing.T) {
	cases := map[string]string{
		"":                  "(no output)",
		"  \n\n":            "(no output)",
		"single line":       "single line",
		"first\nsecond":     "first",
		"  first  \nsecond": "first",
	}
	for input, want := range cases {
		if got := firstLine([]byte(input)); got != want {
			t.Errorf("firstLine(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestContainsFold(t *testing.T) {
	if !containsFold("Non-Fast-Forward rejected", "non-fast-forward") {
		t.Error("containsFold should be case-insensitive")
	}
	if containsFold("everything up to date", "rejected") {
		t.Error("containsFold matched a substring that is not present")
	}
}

func TestIsPushConflict(t *testing.T) {
	if !isPushConflict([]byte("! [rejected] HEAD -> master (fetch first)")) {
		t.Error("expected a fetch-first rejection to classify as a conflict")
	}
	if isPushConflict([]byte("fatal: could not resolve host")) {
		t.Error("a DNS failure must not classify as a push conflict")
	}
}

// TestNewRunner_DefaultsToTheRealBinary proves the nil branch returns a
// live execRunner rather than a nil interface a caller would panic on.
func TestNewRunner_DefaultsToTheRealBinary(t *testing.T) {
	r := newRunner(nil)
	if _, ok := r.(execRunner); !ok {
		t.Fatalf("newRunner(nil) = %T, want execRunner", r)
	}
	injected := &redirectingRunner{}
	got, ok := newRunner(injected).(*redirectingRunner)
	if !ok || got != injected {
		t.Fatal("newRunner did not return the injected runner unchanged")
	}
}

// TestDefaultGetenv proves the production Getenv reads the real
// environment (the only way to cover a one-line os.Getenv wrapper
// honestly, per Art.2, is to set a real variable and read it back).
func TestDefaultGetenv(t *testing.T) {
	t.Setenv("CASCADE_WIKI_TEST_PROBE", "present")
	if got := defaultGetenv("CASCADE_WIKI_TEST_PROBE"); got != "present" {
		t.Fatalf("defaultGetenv = %q, want %q", got, "present")
	}
	_ = os.Unsetenv // documents that t.Setenv restores this automatically
}

// TestRequireGit_RealBinaryPresent proves the happy path against
// whatever `git` this machine actually has (the CI/dev machine that runs
// this suite is required to have git — the same assumption
// runner_exec.go's production code makes).
func TestRequireGit_RealBinaryPresent(t *testing.T) {
	if err := requireGit(); err != nil {
		t.Fatalf("requireGit() on a machine with git installed: %v", err)
	}
}
