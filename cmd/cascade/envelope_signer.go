//go:build !windows

// Purpose: the controller installation identity key and the ONE production
//
//	scope-envelope signer over it. The key is provisioned create-only in the
//	vault under the reserved name cascade_installation_key, read back
//	through secrets.InstallationKeyReader, and exposed as an envelope.Signer
//	and its public key (the key nodes pin).
//
// Inputs: the daemon's path provider (its data directory and, through the
//
//	process firewall, its vault broker).
//
// Outputs: productionEnvelopeSigner, installationPublicKey, and the
//
//	fail-closed eligibility check controllerEligible / controllerEligibleWith
//	/ bindingProbeAbsent.
//
// Constraints: FAIL CLOSED. Eligibility is decided on every call, before the
//
//	memo is read, from node-only artifacts: a daemon is eligible only when
//	its controller-binding path is proven absent and the role resolver
//	agrees. Any probe error is a refusal, never "no binding". Custody errors,
//	a corrupt vault and a malformed entry are returned as errors and nothing
//	is generated or written over them. The private key lives in vault custody
//	and in this process's memory only; nothing here exports, logs or formats
//	it. Unix only: the controller daemon and the daemon-key lock it shares
//	with the approval key do not exist on Windows (tier-2, no daemon).
//
// SPORT: cmd/cascade installation identity/ADDED (P1-SEC-40).

package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"

	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
	scopeenvelope "github.com/acamarata/cascade/pkg/envelope"
)

// errInstallationKeyIneligible is the refusal for a daemon that may not hold
// the controller installation key. Compare by identity on the wrapped chain:
// a cascade error's Is compares Kind only.
var errInstallationKeyIneligible = cascade.New(cascade.KindPermissionDenied,
	"installation key: only the controller daemon holds the installation identity key")

// installationSignerState memoizes the provisioned signer for the process.
// A mutex rather than sync.Once: a failure is never cached. It holds key
// material only, never a role, an eligibility verdict or an error.
var installationSignerState struct {
	mu     sync.Mutex
	signer *installationSigner
}

// installationSigner is the envelope.Signer over the installation key. Its
// fields are unexported and it formats as a fingerprint only, so the private
// half cannot reach a log line, an error or an encoder.
type installationSigner struct {
	pub  ed25519.PublicKey
	priv ed25519.PrivateKey
}

var _ scopeenvelope.Signer = (*installationSigner)(nil)

// PublicKey returns a copy of the public half.
func (s *installationSigner) PublicKey() ed25519.PublicKey {
	if s == nil {
		return nil
	}
	return append(ed25519.PublicKey(nil), s.pub...)
}

// Sign returns the ed25519 signature of msg under the installation key.
func (s *installationSigner) Sign(ctx context.Context, msg []byte) ([]byte, error) {
	if s == nil || len(s.priv) != ed25519.PrivateKeySize {
		return nil, cascade.New(cascade.KindUnavailable, "installation key: no signer")
	}
	if err := ctx.Err(); err != nil {
		return nil, cascade.Wrap(cascade.KindCanceled, err, "installation key: sign")
	}
	return ed25519.Sign(s.priv, msg), nil
}

// Format prints the signer as a redacted marker for every verb, so %v, %+v,
// %#v, %s and %x never print key bytes. A value receiver, so a dereferenced
// signer is redacted too.
func (installationSigner) Format(f fmt.State, _ rune) {
	_, _ = io.WriteString(f, "installationSigner{redacted}")
}

// productionEnvelopeSigner returns the process's scope-envelope signer over
// the installation key, provisioning the key create-only on first use.
// controllerEligible runs on EVERY call before the memo is read; a daemon
// that is not the controller is refused KindPermissionDenied and nothing is
// read or written. Custody errors come back unchanged.
func productionEnvelopeSigner(ctx context.Context, paths runtime.PathProvider) (scopeenvelope.Signer, error) {
	s, err := installationSignerFor(ctx, paths)
	if err != nil {
		return nil, err
	}
	return s, nil
}

// installationPublicKey is the public half of productionEnvelopeSigner, for
// node-binding pinning and doctor. It takes the same path (the same
// per-call eligibility check, the same refusals); there is no route to the
// key around it.
func installationPublicKey(ctx context.Context, paths runtime.PathProvider) (ed25519.PublicKey, error) {
	s, err := installationSignerFor(ctx, paths)
	if err != nil {
		return nil, err
	}
	return s.PublicKey(), nil
}

// installationSignerFor is the shared body: eligibility, then the memo, then
// provisioning. It never returns a nil signer with a nil error.
func installationSignerFor(ctx context.Context, paths runtime.PathProvider) (*installationSigner, error) {
	if ok, reason := controllerEligible(paths); !ok {
		return nil, cascade.Wrapf(cascade.KindPermissionDenied, errInstallationKeyIneligible,
			"installation key: refusing to provision or read it: %s", reason)
	}
	installationSignerState.mu.Lock()
	defer installationSignerState.mu.Unlock()
	if s := installationSignerState.signer; s != nil {
		return s, nil
	}
	s, err := provisionInstallationSigner(ctx, paths)
	if err != nil {
		return nil, err
	}
	installationSignerState.signer = s
	return s, nil
}

// provisionInstallationSigner provisions the key (create-only, under the
// daemon-key lock of the process firewall's custody), reads the private half
// through the named non-elevated reader, and builds the signer only when the
// reader's key is the one provisioning reported.
func provisionInstallationSigner(ctx context.Context, paths runtime.PathProvider) (*installationSigner, error) {
	fw, err := processFirewallFor(paths)
	if err != nil {
		return nil, err
	}
	broker := fw.Broker()
	// One lock per custody for every daemon key: the approval key's
	// provisioning takes the same one, so two processes never both create.
	lockPath, err := approvalKeyLockPath(paths.DataDir(), broker.Backend())
	if err != nil {
		return nil, err
	}
	_, pub, _, err := secrets.ProvisionDaemonKey(ctx, broker.WithDaemonKeyLock(lockPath), secrets.InstallationKeyName, rand.Reader)
	if err != nil {
		return nil, err
	}
	reader, err := secrets.NewInstallationKeyReader(broker)
	if err != nil {
		return nil, err
	}
	_, priv, err := reader.InstallationKey(ctx)
	if err != nil {
		return nil, err
	}
	if !keyPairMatches(priv, pub) {
		clear(priv)
		return nil, cascade.New(cascade.KindIntegrity,
			"installation key: the key read back is not the key provisioning reported; refusing to sign with it")
	}
	return &installationSigner{pub: append(ed25519.PublicKey(nil), pub...), priv: priv}, nil
}

// keyPairMatches reports whether priv is a consistent ed25519 key (its
// embedded public half derives from its seed) whose public half is pub.
func keyPairMatches(priv ed25519.PrivateKey, pub ed25519.PublicKey) bool {
	if len(priv) != ed25519.PrivateKeySize || len(pub) != ed25519.PublicKeySize {
		return false
	}
	derived := ed25519.NewKeyFromSeed(priv.Seed())
	defer clear(derived)
	return subtle.ConstantTimeCompare(derived, priv) == 1 &&
		subtle.ConstantTimeCompare(priv[ed25519.SeedSize:], pub) == 1
}

// controllerEligible is the fail-closed controller check over the real
// filesystem. P1-NODE-16 replaces it with contract:controller-designation.
func controllerEligible(paths runtime.PathProvider) (bool, string) {
	return controllerEligibleWith(paths, os.Lstat)
}

// controllerEligibleWith decides eligibility on node-only artifacts. It is
// eligible ONLY when the controller-binding path is proven absent (lstat, so
// any entry there, a dangling symlink included, is present) and the role
// resolver agrees. It never trusts the resolver alone: an empty-endpoint
// binding resolves as controller there and is refused here. Every probe
// error is classified by bindingProbeAbsent; a nil provider and an empty or
// relative data directory are not eligible.
func controllerEligibleWith(paths runtime.PathProvider, lstat func(string) (fs.FileInfo, error)) (bool, string) {
	if paths == nil {
		return false, "no path provider: the data directory is unknown"
	}
	dir := paths.DataDir()
	if dir == "" || !filepath.IsAbs(dir) {
		return false, "the data directory is not an absolute path"
	}
	if lstat == nil {
		return false, "no binding probe"
	}
	_, err := lstat(filepath.Join(dir, "nodes", "controller_binding.json"))
	if absent, reason := bindingProbeAbsent(err); !absent {
		return false, "the controller binding is not proven absent: " + reason
	}
	return controllerByResolver(nodes.NewFileControllerBindingBackend(dir))
}

// controllerByResolver is the second, agreeing half: the node-records store
// opened from the data directory must exist and resolve to controller.
// Unlike nodes.ResolveRole, a nil store is a refusal, not "controller".
func controllerByResolver(binding nodes.ControllerBindingBackend) (bool, string) {
	if binding == nil {
		return false, "no node-records store: the role cannot be proven"
	}
	role, err := nodes.ResolveRole(binding)
	if err != nil {
		// A resolver error is never "not a node", whatever it wraps.
		if absent, reason := bindingProbeAbsent(err); !absent {
			return false, "resolving the daemon role: " + reason
		}
		return false, "resolving the daemon role: the binding changed during the probe"
	}
	if role != nodes.RoleController {
		return false, fmt.Sprintf("the daemon role resolves to %q", role)
	}
	return true, ""
}

// bindingProbeAbsent classifies a binding probe error: absent only when it
// is fs.ErrNotExist; nil means an entry is present; anything else is not
// absent, and the reason names it (the operation and the error, not the
// path, so no directory reaches an RPC error).
func bindingProbeAbsent(err error) (absent bool, reason string) {
	switch {
	case err == nil:
		return false, "an entry exists at the controller binding path"
	case errors.Is(err, fs.ErrNotExist):
		return true, ""
	}
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return false, pe.Op + ": " + pe.Err.Error()
	}
	return false, err.Error()
}
