package conform

import (
	"errors"
	"testing"
)

// mockClient is an in-memory fake SimulatedClient.
type mockClient struct {
	calls []string
	err   error
}

func (m *mockClient) Call(method string) error {
	m.calls = append(m.calls, method)
	return m.err
}

// mockConsumer is an in-memory fake ReferenceConsumer.
type mockConsumer struct {
	handles []string
	err     error
}

func (m *mockConsumer) Handle(method string) error {
	m.handles = append(m.handles, method)
	return m.err
}

// TestConformEmptyChecksFails validates that nil/empty checks, duplicate names/methods,
// empty fields, or nil collaborators return only configuration failure with ErrInvalidChecks.
func TestConformEmptyChecksFails(t *testing.T) {
	tests := []struct {
		name     string
		client   SimulatedClient
		consumer ReferenceConsumer
		checks   []Check
	}{
		{
			name:     "nil client",
			client:   nil,
			consumer: &mockConsumer{},
			checks:   DefaultChecks(),
		},
		{
			name:     "nil consumer",
			client:   &mockClient{},
			consumer: nil,
			checks:   DefaultChecks(),
		},
		{
			name:     "nil checks",
			client:   &mockClient{},
			consumer: &mockConsumer{},
			checks:   nil,
		},
		{
			name:     "empty checks",
			client:   &mockClient{},
			consumer: &mockConsumer{},
			checks:   []Check{},
		},
		{
			name:     "empty name",
			client:   &mockClient{},
			consumer: &mockConsumer{},
			checks:   []Check{{Name: "", Method: "Ping"}},
		},
		{
			name:     "empty method",
			client:   &mockClient{},
			consumer: &mockConsumer{},
			checks:   []Check{{Name: "ping", Method: ""}},
		},
		{
			name:     "duplicate names",
			client:   &mockClient{},
			consumer: &mockConsumer{},
			checks:   []Check{{Name: "ping", Method: "Ping"}, {Name: "ping", Method: "Pong"}},
		},
		{
			name:     "duplicate methods",
			client:   &mockClient{},
			consumer: &mockConsumer{},
			checks:   []Check{{Name: "ping1", Method: "Ping"}, {Name: "ping2", Method: "Ping"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			report := RunConformance(tt.client, tt.consumer, tt.checks)

			if report.Passed {
				t.Error("expected report to fail")
			}

			if len(report.Results) != 1 {
				t.Errorf("expected 1 result, got %d", len(report.Results))
			}

			if len(report.Results) > 0 {
				if report.Results[0].Name != "configuration" {
					t.Errorf("expected result name 'configuration', got %q", report.Results[0].Name)
				}
				if report.Results[0].Passed {
					t.Error("expected result to fail")
				}
				if !errors.Is(report.Results[0].Err, ErrInvalidChecks) {
					t.Errorf("expected ErrInvalidChecks, got %v", report.Results[0].Err)
				}
			}

			// Verify no calls were made
			if c, ok := tt.client.(*mockClient); ok && c != nil {
				if len(c.calls) > 0 {
					t.Errorf("expected zero calls, got %d", len(c.calls))
				}
			}
			if c, ok := tt.consumer.(*mockConsumer); ok && c != nil {
				if len(c.handles) > 0 {
					t.Errorf("expected zero handles, got %d", len(c.handles))
				}
			}
		})
	}
}

// TestConformSimulatedClientOnly validates that the runner invokes only injected fake
// Call/Handle using exact supplied methods, DefaultChecks returns independent slices,
// and no network or real CLI is used.
func TestConformSimulatedClientOnly(t *testing.T) {
	// Test that DefaultChecks returns independent slices
	checks1 := DefaultChecks()
	checks2 := DefaultChecks()

	if &checks1[0] == &checks2[0] {
		t.Error("DefaultChecks should return independent slices")
	}

	// Modify checks1 and verify checks2 is unaffected
	if len(checks1) > 0 {
		checks1[0].Name = "modified"
		if len(checks2) > 0 && checks2[0].Name == "modified" {
			t.Error("modifying one slice should not affect another")
		}
	}

	// Test with custom checks
	client := &mockClient{}
	consumer := &mockConsumer{}

	customChecks := []Check{
		{Name: "check1", Method: "Custom.Method1"},
		{Name: "check2", Method: "Custom.Method2"},
	}

	report := RunConformance(client, consumer, customChecks)

	if !report.Passed {
		t.Error("expected report to pass")
	}

	if len(report.Results) != 2 {
		t.Errorf("expected 2 results, got %d", len(report.Results))
	}

	// Verify exact methods were called
	expectedMethods := []string{"Custom.Method1", "Custom.Method2"}
	if len(client.calls) != len(expectedMethods) {
		t.Errorf("expected %d calls, got %d", len(expectedMethods), len(client.calls))
	}

	for i, method := range expectedMethods {
		if i >= len(client.calls) {
			break
		}
		if client.calls[i] != method {
			t.Errorf("expected call[%d] to be %q, got %q", i, method, client.calls[i])
		}
	}

	// Verify exact methods were handled
	if len(consumer.handles) != len(expectedMethods) {
		t.Errorf("expected %d handles, got %d", len(expectedMethods), len(consumer.handles))
	}

	for i, method := range expectedMethods {
		if i >= len(consumer.handles) {
			break
		}
		if consumer.handles[i] != method {
			t.Errorf("expected handle[%d] to be %q, got %q", i, method, consumer.handles[i])
		}
	}

	// Test with default checks
	client2 := &mockClient{}
	consumer2 := &mockConsumer{}
	defaultChecks := DefaultChecks()

	report2 := RunConformance(client2, consumer2, defaultChecks)

	if !report2.Passed {
		t.Error("expected report with default checks to pass")
	}

	expectedDefaultMethods := []string{"Ping", "Lease.Attest", "Lease.Renew"}
	if len(client2.calls) != len(expectedDefaultMethods) {
		t.Errorf("expected %d default calls, got %d", len(expectedDefaultMethods), len(client2.calls))
	}

	for i, method := range expectedDefaultMethods {
		if i >= len(client2.calls) {
			break
		}
		if client2.calls[i] != method {
			t.Errorf("expected default call[%d] to be %q, got %q", i, method, client2.calls[i])
		}
	}
}
