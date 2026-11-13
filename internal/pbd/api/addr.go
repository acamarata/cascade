// Package api defines the PBD API server listen address and its loopback-only validation.
package api

import (
	"errors"
	"net"
)

// DefaultAddr is the default listen address for the API server,
// binding to the loopback interface on port 5274.
const DefaultAddr = "127.0.0.1:5274"

// ValidateAddr validates that addr is the allowed loopback endpoint
// "127.0.0.1:5274". It rejects all other hosts and ports.
func ValidateAddr(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
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
