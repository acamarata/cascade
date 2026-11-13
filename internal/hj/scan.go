// Package hj implements broker job scanning.
package hj

import "fmt"

// ScanState represents the state of a job scan.
type ScanState struct {
	// Done indicates whether the scan has completed successfully.
	Done bool
}

// ScanFunc is a function that performs a job scan and returns its state.
type ScanFunc func() (ScanState, error)

// Broker manages job scanning and execution.
type Broker struct {
	// Scan is the function used to perform the initial job scan.
	Scan ScanFunc
}

// Start performs the initial job scan synchronously and blocks until it completes.
// If the scan fails, it returns an error wrapped with the M0881 requirement code.
func (b Broker) Start() (ScanState, error) {
	state, err := b.Scan()
	if err != nil {
		return ScanState{}, fmt.Errorf("M0881: first scan failed: %w", err)
	}
	return state, nil
}
