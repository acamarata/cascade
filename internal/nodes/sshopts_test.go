package nodes

import "testing"

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
	var _ = o.BatchMode
	var _ = o.ConnectTimeout
	var _ = o.DisableAgentForwarding
	var _ = o.KnownHostsPolicy
	var _ = o.KeepAlive
}
