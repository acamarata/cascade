// Package api defines the PBD API server listen address and its loopback-only validation.
package api

import (
	"errors"
	"strings"
)

// DefaultAddr is the default listen address for the API server,
// binding to the loopback interface on port 5274.
const DefaultAddr = "127.0.0.1:5274"

// ValidateAddr validates that addr is the allowed loopback endpoint
// "127.0.0.1:5274". It rejects all other hosts and ports.
func ValidateAddr(addr string) error {
	// Parsed by hand: the package only checks one fixed loopback endpoint, so it stays off the egress network list.
	host, port, ok := strings.Cut(addr, ":")
	if !ok || strings.Contains(port, ":") {
		return errors.New("invalid address format")
	}
	if host != "127.0.0.1" {
		return errors.New("host must be 127.0.0.1")
	}
	if port != "5274" {
		return errors.New("port must be 5274")
	}
	return nil
}
