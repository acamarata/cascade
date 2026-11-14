// Package redactnames provides a fixed list of exported identifiers
// for the Redact module and utilities to verify their stability.
package redactnames

import (
	"fmt"
	"sort"
	"strings"
)

// pinned is the literal slice of exported identifiers for the Redact module.
var pinned = []string{"Redact"}

// Names returns a sorted copy of the pinned list of exported identifiers
// and context-cued class names for the Redact module.
func Names() []string {
	res := make([]string, len(pinned))
	copy(res, pinned)
	sort.Strings(res)
	return res
}

// Check compares a supplied list of names against the pinned list and
// returns an error naming each missing or renamed identifier. Extra new
// names in current are allowed; removals fail. The error text is
// 'missing or renamed: X' per name, joined by commas.
func Check(current []string) error {
	have := make(map[string]bool, len(current))
	for _, n := range current {
		have[n] = true
	}
	var missing []string
	for _, want := range pinned {
		if !have[want] {
			missing = append(missing, want)
		}
	}
	if len(missing) > 0 {
		msgs := make([]string, len(missing))
		for i, m := range missing {
			msgs[i] = fmt.Sprintf("missing or renamed: %s", m)
		}
		return fmt.Errorf("%s", strings.Join(msgs, ", "))
	}
	return nil
}
