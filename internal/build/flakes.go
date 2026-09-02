// Package build (this file) implements the flake-quarantine registry from
// P1-E01-W1-S01-T8 (12-QUALITY-CONSTITUTION.md Art.7.5 + Art.6): "a test
// that flakes twice is quarantined WITH a ticket, never retried away";
// quarantine entries carry an expiry (Art.6: "any gate that is
// non-blocking must carry an owning ticket and an expiry").
//
// This is a REGISTRY, not a flake DETECTOR — nothing in this ticket's
// scope re-runs the suite N times hunting for flakiness (that is its own,
// separate mechanism, likely a scheduled CI job, not this quality-gate
// package). What this file enforces is the STRUCTURE of quarantine once a
// flake has been observed and someone deliberately quarantines it: every
// entry in the committed registry must name the exact test, its owning
// ticket, and a non-past expiry — a registry entry that has silently
// expired, or that quarantines a test with no ticket, is exactly the "flake
// retried away forever" failure mode Art.7.5 exists to prevent, so an
// EXPIRED or malformed entry is itself a gate violation, not a quiet pass.
package build

import (
	"encoding/json"
	"fmt"
	"regexp"
	"time"
)

// FlakeEntry is one quarantined test.
type FlakeEntry struct {
	// Test is the fully-qualified Go test identifier: "<package
	// import path>.<TestName>" (matches how `go test -run` addresses a
	// single test unambiguously across packages).
	Test string `json:"test"`
	// Ticket is the owning ticket id (this plan's grammar, e.g.
	// "P1-E01-W1-S01-T8") — Art.7.5's "never retried away" requirement:
	// a quarantine with no ticket is not a quarantine, it is a silence.
	Ticket string `json:"ticket"`
	// Reason is a short, human-readable note on what flakes and why it
	// is believed to (never empty).
	Reason string `json:"reason"`
	// Expiry is an RFC 3339 date (YYYY-MM-DD) after which this entry is
	// itself a gate violation (Art.6) — quarantine is a deadline, not a
	// permanent exemption.
	Expiry string `json:"expiry"`
}

// flakeTicketIDPattern mirrors stubgateTicketIDPattern — the same plan
// contract-id grammar, reused here so both gates agree on what "an owning
// ticket" syntactically looks like.
var flakeTicketIDPattern = regexp.MustCompile(`^P\d+-[A-Z]+\d*-W\d+-S\d+-T\d+$`)

// FlakeRegistryViolation is one malformed or expired registry entry.
type FlakeRegistryViolation struct {
	Test   string
	Reason string
}

// ParseFlakeRegistry decodes the registry format: a JSON array of
// FlakeEntry.
func ParseFlakeRegistry(data []byte) ([]FlakeEntry, error) {
	var out []FlakeEntry
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("flake registry: parsing: %w", err)
	}
	return out, nil
}

// ValidateFlakeRegistry checks every entry's structural completeness
// (Test/Ticket/Reason non-empty, Ticket matches the plan's id grammar) and
// its expiry against now — an ENTRY WHOSE EXPIRY HAS PASSED is reported as
// a violation, exactly like a missing ticket: quarantine that outlives its
// own deadline is the "retried away forever" failure this gate exists to
// name and stop.
func ValidateFlakeRegistry(entries []FlakeEntry, now time.Time) []FlakeRegistryViolation {
	var out []FlakeRegistryViolation
	seen := make(map[string]bool, len(entries))
	for _, e := range entries {
		if e.Test == "" {
			out = append(out, FlakeRegistryViolation{Test: e.Test, Reason: "empty test identifier"})
			continue
		}
		if seen[e.Test] {
			out = append(out, FlakeRegistryViolation{Test: e.Test, Reason: "duplicate entry for the same test"})
		}
		seen[e.Test] = true

		if !flakeTicketIDPattern.MatchString(e.Ticket) {
			out = append(out, FlakeRegistryViolation{Test: e.Test, Reason: fmt.Sprintf("ticket %q does not match the plan id grammar", e.Ticket)})
		}
		if e.Reason == "" {
			out = append(out, FlakeRegistryViolation{Test: e.Test, Reason: "empty reason"})
		}
		expiry, err := time.Parse("2006-01-02", e.Expiry)
		if err != nil {
			out = append(out, FlakeRegistryViolation{Test: e.Test, Reason: fmt.Sprintf("expiry %q is not a valid YYYY-MM-DD date: %v", e.Expiry, err)})
			continue
		}
		if !expiry.After(now) {
			out = append(out, FlakeRegistryViolation{Test: e.Test, Reason: fmt.Sprintf("quarantine expired %s — the test must be fixed, re-quarantined with a fresh expiry, or removed from the suite", e.Expiry)})
		}
	}
	return out
}

// IsQuarantined reports whether testID (the same "<import path>.<TestName>"
// shape FlakeEntry.Test uses) has a live (non-expired) quarantine entry —
// callers that want to skip a quarantined test at run time (not this
// ticket's scope: no test in this repo is quarantined today) consult this
// against the parsed, validated registry.
func IsQuarantined(entries []FlakeEntry, testID string, now time.Time) bool {
	for _, e := range entries {
		if e.Test != testID {
			continue
		}
		expiry, err := time.Parse("2006-01-02", e.Expiry)
		if err != nil {
			continue
		}
		if expiry.After(now) {
			return true
		}
	}
	return false
}
