package nodes

import (
	"testing"
	"time"
)

func TestOptionsFixedSet(t *testing.T) {
	opts, err := NewSSHOptions()
	if err != nil {
		t.Fatalf("NewSSHOptions() returned error: %v", err)
	}
	if opts != nil {
		t.Fatalf("expected nil SSHOptions from stub, got %v", opts)
	}

	// Ensure fields exist and have correct types
	var o SSHOptions
	var _ bool = o.BatchMode
	var _ time.Duration = o.ConnectTimeout
	var _ bool = o.DisableAgentForwarding
	var _ string = o.KnownHostsPolicy
	var _ bool = o.KeepAlive
}
