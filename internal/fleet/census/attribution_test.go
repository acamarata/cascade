package census

import "testing"

// Purpose: attributeAccount's pure-function contract — known match,
//
//	unresolvable/unknown, ambiguous (must ALSO resolve to unknown, never
//	guessed), and the no-global-state property.
//
// SPORT: fleet/census (ADD, per T-1 sport_updates).

func TestAttributeAccountKnownMatch(t *testing.T) {
	dirs := map[string]string{
		"/home/acct-a/.cascade": "acct-a",
		"/home/acct-b/.cascade": "acct-b",
	}
	argv := []string{"/usr/local/bin/claude", "--home=/home/acct-b/.cascade", "--json"}
	got := attributeAccount(argv, dirs)
	if got != "acct-b" {
		t.Fatalf("attributeAccount = %q, want acct-b", got)
	}
}

func TestAttributeAccountUnknown(t *testing.T) {
	dirs := map[string]string{"/home/acct-a/.cascade": "acct-a"}
	argv := []string{"/usr/local/bin/codex", "--print"}
	if got := attributeAccount(argv, dirs); got != "" {
		t.Fatalf("attributeAccount = %q, want empty (unknown)", got)
	}
}

func TestAttributeAccountEmptyInputs(t *testing.T) {
	if got := attributeAccount(nil, nil); got != "" {
		t.Fatalf("attributeAccount(nil, nil) = %q, want empty", got)
	}
	if got := attributeAccount([]string{"claude"}, map[string]string{}); got != "" {
		t.Fatalf("attributeAccount with empty dirs = %q, want empty", got)
	}
}

// TestAttributeAccountAmbiguousIsUnknown is the non-negotiable: a process
// whose argv matches more than one distinct account alias must resolve to
// "" (unknown), never one of the matches picked arbitrarily.
func TestAttributeAccountAmbiguousIsUnknown(t *testing.T) {
	dirs := map[string]string{
		"/shared/accounts/a": "acct-a",
		"/shared/accounts/b": "acct-b",
	}
	argv := []string{"/usr/local/bin/opencode", "--config=/shared/accounts/a:/shared/accounts/b"}
	got := attributeAccount(argv, dirs)
	if got != "" {
		t.Fatalf("attributeAccount (ambiguous) = %q, want empty (unknown)", got)
	}
}

// TestAttributeAccountDeterministic proves the ambiguity rule holds
// regardless of Go's randomized map iteration order, by running the same
// ambiguous case many times.
func TestAttributeAccountDeterministic(t *testing.T) {
	dirs := map[string]string{
		"/shared/accounts/a": "acct-a",
		"/shared/accounts/b": "acct-b",
		"/shared/accounts/c": "acct-c",
	}
	argv := []string{"/usr/local/bin/claude", "/shared/accounts/a", "/shared/accounts/c"}
	for i := 0; i < 50; i++ {
		if got := attributeAccount(argv, dirs); got != "" {
			t.Fatalf("iteration %d: attributeAccount = %q, want empty (ambiguous)", i, got)
		}
	}
}

func TestAttributeAccountIgnoresEmptyMapEntries(t *testing.T) {
	dirs := map[string]string{
		"":                      "should-never-match",
		"/home/acct-a/.cascade": "",
	}
	argv := []string{"/usr/local/bin/claude", "/home/acct-a/.cascade"}
	if got := attributeAccount(argv, dirs); got != "" {
		t.Fatalf("attributeAccount = %q, want empty (both entries are degenerate)", got)
	}
}
