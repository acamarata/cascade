// Purpose: configure the ssh handshake to observe the host key enrollment signs.
// Inputs: the dialer timeout, authentication signer, and host-key verifier.
// Outputs: an ssh client configuration restricted to ssh-ed25519 host keys.
// Constraints: preserve the verifier's typed refusal and the configured timeout.
// SPORT: internal/nodes enrollment host-key binding.

package nodes

import (
	"net"
	"time"

	"golang.org/x/crypto/ssh"
)

func defaultHandshakeTimeout(d time.Duration) time.Duration {
	if d <= 0 {
		return 15 * time.Second
	}
	return d
}

// clientConfig pins the host-key algorithm to the key the node signs at enrollment.
func (d *sshDialer) clientConfig(user string, signer ssh.Signer, verify HostKeyVerifier) *ssh.ClientConfig {
	return &ssh.ClientConfig{
		User:              user,
		Auth:              []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback:   func(_ string, _ net.Addr, key ssh.PublicKey) error { return verify(HostKeyFingerprint(key.Marshal())) },
		HostKeyAlgorithms: []string{ssh.KeyAlgoED25519},
		Timeout:           d.handshakeTimeout,
	}
}
