// Package nodes provides SSH connection option types.
package nodes

import "time"

// SSHOptions holds configuration for an SSH connection.
type SSHOptions struct {
	// BatchMode enables non-interactive batch mode.
	BatchMode bool
	// ConnectTimeout is the timeout for establishing a connection.
	ConnectTimeout time.Duration
	// DisableAgentForwarding disables SSH agent forwarding when true.
	DisableAgentForwarding bool
	// KnownHostsPolicy specifies the pinned known-hosts policy.
	KnownHostsPolicy string
	// KeepAlive enables TCP keep-alive probes.
	KeepAlive bool
}

// NewSSHOptions returns a new SSHOptions instance.
func NewSSHOptions() (*SSHOptions, error) {
	return nil, nil
}
