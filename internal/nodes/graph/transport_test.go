package graph

import "testing"

// TestSelectTransportSSHWhenNoNodesvc verifies that SelectTransport returns "ssh" when nodesvc is not running.
func TestSelectTransportSSHWhenNoNodesvc(t *testing.T) {
	got := SelectTransport(false)
	if got != "ssh" {
		t.Fatalf("SelectTransport(false) = %q, want %q", got, "ssh")
	}
}

// TestSelectTransportNodesvcWhenRunning verifies that SelectTransport returns "nodesvc" when nodesvc is running.
func TestSelectTransportNodesvcWhenRunning(t *testing.T) {
	got := SelectTransport(true)
	if got != "nodesvc" {
		t.Fatalf("SelectTransport(true) = %q, want %q", got, "nodesvc")
	}
}

// TestSSHTransportAlwaysAvailable verifies that the SSH transport always reports available.
func TestSSHTransportAlwaysAvailable(t *testing.T) {
	tr := &SSHTransport{}
	if !tr.Available() {
		t.Fatal("SSHTransport.Available() = false, want true")
	}
}
