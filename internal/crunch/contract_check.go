package crunch

import (
	"errors"
	"regexp"
	"sync"
)

// ErrUnresolvedContract is returned when a ticket references a contract
// that is not in the Provided set. The ticket should be held as
// needs-contract rather than failed.
var ErrUnresolvedContract = errors.New("unresolved contract reference")

var (
	retryMu    sync.Mutex
	retryState = map[string]int{}
)

// SymbolChecker verifies that all contract symbols referenced in a ticket
// are present in the Provided map.
type SymbolChecker struct {
	// Provided maps contract symbol names to true for contracts that are
	// available and resolved.
	Provided map[string]bool
}

// contractSymbolRegex matches contract symbols prefixed with @.
var contractSymbolRegex = regexp.MustCompile(`@([A-Za-z_][A-Za-z0-9_]*)`)

// CheckSymbols scans the ticket text for contract references (symbols
// prefixed with @) and checks whether each referenced contract exists in
// the Provided map. If any referenced contract is not provided, it returns
// ErrUnresolvedContract. Symbols are checked only against the Provided
// contracts; references to contracts not in Provided cause an error so the
// ticket can be held as needs-contract.
func (s *SymbolChecker) CheckSymbols(ticket string) error {
	matches := contractSymbolRegex.FindAllStringSubmatch(ticket, -1)

	seen := map[string]bool{}
	for _, match := range matches {
		if len(match) < 2 {
			continue
		}
		symbol := match[1]
		if seen[symbol] {
			continue
		}
		seen[symbol] = true

		if !s.Provided[symbol] {
			return ErrUnresolvedContract
		}
	}

	return nil
}

// ResetRetries clears the contract retry state, keeping retries off the
// strike count. This ensures that contract resolution attempts do not
// accumulate against failure thresholds.
func ResetRetries() {
	retryMu.Lock()
	defer retryMu.Unlock()
	retryState = map[string]int{}
}

// RecordRetry records a retry attempt for a contract symbol.
func RecordRetry(symbol string) {
	retryMu.Lock()
	defer retryMu.Unlock()
	retryState[symbol]++
}

// GetRetryCount returns the number of retry attempts for a contract symbol.
func GetRetryCount(symbol string) int {
	retryMu.Lock()
	defer retryMu.Unlock()
	return retryState[symbol]
}
