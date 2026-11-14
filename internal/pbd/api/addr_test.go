package api

import "testing"

// TestDefaultAddrValue ensures the API default listen address is the
// required loopback endpoint.
func TestDefaultAddrValue(t *testing.T) {
	want := "127.0.0.1:5274"
	if DefaultAddr != want {
		t.Fatalf("DefaultAddr = %q, want %q", DefaultAddr, want)
	}
}

// TestValidateAddrAcceptsLoopback ensures the required loopback endpoint is
// accepted.
func TestValidateAddrAcceptsLoopback(t *testing.T) {
	if err := ValidateAddr("127.0.0.1:5274"); err != nil {
		t.Fatalf("ValidateAddr(127.0.0.1:5274) = %v, want nil", err)
	}
}

// TestValidateAddrRejectsAllInterfaces ensures an address that binds all
// interfaces is rejected.
func TestValidateAddrRejectsAllInterfaces(t *testing.T) {
	if err := ValidateAddr("0.0.0.0:5274"); err == nil {
		t.Fatal("ValidateAddr(0.0.0.0:5274) = nil, want error")
	}
}

// TestValidateAddrRejectsOtherPort ensures the loopback host with another
// port is rejected.
func TestValidateAddrRejectsOtherPort(t *testing.T) {
	if err := ValidateAddr("127.0.0.1:8080"); err == nil {
		t.Fatal("ValidateAddr(127.0.0.1:8080) = nil, want error")
	}
}
