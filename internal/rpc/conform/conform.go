// Package conform provides conformance checking contracts and runners.
package conform

import (
	"errors"
	"reflect"
)

// ErrInvalidChecks is returned when the provided checks or collaborators are invalid.
var ErrInvalidChecks = errors.New("conform: invalid checks")

// SimulatedClient represents a client that can invoke methods by name.
type SimulatedClient interface {
	// Call invokes the named method on the client.
	Call(method string) error
}

// ReferenceConsumer represents a consumer that handles method results.
type ReferenceConsumer interface {
	// Handle processes the named method result.
	Handle(method string) error
}

// Check defines a single conformance check to run.
type Check struct {
	// Name is the unique identifier for this check.
	Name string
	// Method is the RPC method to invoke.
	Method string
}

// CheckResult holds the outcome of a single conformance check.
type CheckResult struct {
	// Name identifies which check produced this result.
	Name string
	// Passed indicates whether the check succeeded.
	Passed bool
	// Err holds any error encountered during the check.
	Err error
}

// Report aggregates the results of all conformance checks.
type Report struct {
	// Results contains the outcome of each individual check.
	Results []CheckResult
	// Passed indicates whether all checks succeeded.
	Passed bool
}

// ExitCode returns 0 if the report passed, 1 otherwise.
func (r Report) ExitCode() int {
	if r.Passed {
		return 0
	}
	return 1
}

// DefaultChecks returns a fresh fixed ordered list of standard conformance checks.
func DefaultChecks() []Check {
	return []Check{
		{Name: "ping", Method: "Ping"},
		{Name: "attest", Method: "Lease.Attest"},
		{Name: "renew", Method: "Lease.Renew"},
	}
}

// RunConformance executes the given checks using the provided client and consumer.
// It validates inputs before invoking anything, returning a configuration failure
// if client, consumer, or checks are invalid.
func RunConformance(client SimulatedClient, consumer ReferenceConsumer, checks []Check) Report {
	if err := validate(client, consumer, checks); err != nil {
		return Report{
			Results: []CheckResult{{Name: "configuration", Passed: false, Err: ErrInvalidChecks}},
			Passed:  false,
		}
	}

	results := make([]CheckResult, 0, len(checks))
	passed := true

	for _, c := range checks {
		callErr := client.Call(c.Method)
		handleErr := consumer.Handle(c.Method)

		var combined error
		if callErr != nil {
			combined = callErr
		} else if handleErr != nil {
			combined = handleErr
		}

		cr := CheckResult{
			Name:   c.Name,
			Passed: combined == nil,
			Err:    combined,
		}
		if !cr.Passed {
			passed = false
		}
		results = append(results, cr)
	}

	return Report{Results: results, Passed: passed}
}

func validate(client SimulatedClient, consumer ReferenceConsumer, checks []Check) error {
	if isNilInterface(client) || isNilInterface(consumer) {
		return ErrInvalidChecks
	}
	if len(checks) == 0 {
		return ErrInvalidChecks
	}

	names := make(map[string]struct{}, len(checks))
	methods := make(map[string]struct{}, len(checks))

	for _, c := range checks {
		if c.Name == "" || c.Method == "" {
			return ErrInvalidChecks
		}
		if _, exists := names[c.Name]; exists {
			return ErrInvalidChecks
		}
		if _, exists := methods[c.Method]; exists {
			return ErrInvalidChecks
		}
		names[c.Name] = struct{}{}
		methods[c.Method] = struct{}{}
	}

	return nil
}

func isNilInterface(v any) bool {
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	return rv.Kind() == reflect.Ptr && rv.IsNil()
}
