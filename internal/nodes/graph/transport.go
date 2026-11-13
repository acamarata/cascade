// Package graph provides node graph management and transport selection.
package graph

// Transport represents a nodelink transport mechanism.
type Transport interface {
	// Available reports whether the transport is currently usable.
	Available() bool
}

// SSHTransport represents the SSH nodelink transport.
type SSHTransport struct{}

// Available reports whether the SSH transport is usable. It always returns true.
func (s *SSHTransport) Available() bool {
	return true
}

// SelectTransport returns the preferred transport name based on whether nodesvc is running.
// It returns "nodesvc" when nodesvcRunning is true and "ssh" when false,
// keeping SSH usable as the nodelink fallback until the node starts nodesvc.
func SelectTransport(nodesvcRunning bool) string {
	if nodesvcRunning {
		return "nodesvc"
	}
	return "ssh"
}
