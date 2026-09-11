// Purpose: the known_hosts store internal/nodes owns per R-21.220 —
//
//	pinned ssh host-key fingerprints, and the fail-closed check every
//	enrollment and reconnect attempt runs before proceeding.
//
// Inputs: a host identifier (user@host, or whatever addressing S-36.T4's
//
//	CLI surface passes through) and the host's public key bytes, as
//	reported by whichever transport dials it (S-36.T3 — this ticket does
//	not dial ssh itself, see the package doc's BOUNDARY note).
//
// Outputs: an accept/refuse decision, fail-closed on anything this store
//
//	cannot positively confirm.
//
// Constraints: R-21.220 — "enrollment runs over ssh with STRICT host-key
//
//	checking against a known_hosts store owned by internal/nodes. An
//	unknown or changed host key is a typed fail-closed error unless an
//	explicit --host-key-fingerprint <sha256> matches." This file is the
//	pinning logic (parse/store/verify fingerprints); it deliberately does
//	NOT import net or dial anything — the default unit lane forbids
//	importing net, and the actual ssh session is S-36.T3's transport
//	layer, which will call VerifyHostKey with the key bytes it receives
//	mid-handshake.
//
// SPORT: internal/nodes KnownHosts/ADDED (P1-E17-W4-S36-T1).

package nodes

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// HostKeyFingerprint returns the SHA-256 hex digest of a host's raw public
// key bytes — the format `--host-key-fingerprint <sha256>` accepts and the
// format this store persists.
func HostKeyFingerprint(hostPubKey []byte) string {
	sum := sha256.Sum256(hostPubKey)
	return hex.EncodeToString(sum[:])
}

// KnownHostsBackend persists pinned host->fingerprint pairs. fileBackend is
// the production implementation.
type KnownHostsBackend interface {
	Load() (map[string]string, error)
	Save(map[string]string) error
}

// KnownHosts is the ssh host-key pinning store this package owns.
type KnownHosts struct {
	backend KnownHostsBackend
}

// NewKnownHosts constructs a store over backend.
func NewKnownHosts(backend KnownHostsBackend) *KnownHosts {
	return &KnownHosts{backend: backend}
}

// ErrHostKeyUnknown reports that host has no pinned fingerprint and no
// matching --host-key-fingerprint was supplied out-of-band.
// KindPermissionDenied: the caller lacks the proof this operation
// requires (either a prior pin or an explicit out-of-band confirmation),
// and no elevation path exists here beyond supplying that fingerprint.
func ErrHostKeyUnknown(host string) error {
	return cascade.Newf(cascade.KindPermissionDenied,
		"nodes: unknown ssh host key for %q; pin it with --host-key-fingerprint <sha256> after out-of-band confirmation", host)
}

// ErrHostKeyChanged reports that host's presented key does not match its
// pinned fingerprint, and no matching --host-key-fingerprint override was
// supplied. This is refused unconditionally on reconnect (S-36.T3 never
// silently re-pins).
func ErrHostKeyChanged(host string) error {
	return cascade.Newf(cascade.KindPermissionDenied,
		"nodes: ssh host key for %q has changed since it was pinned; refusing (possible MITM); supply the confirmed --host-key-fingerprint to re-pin", host)
}

// Verify checks presentedFingerprint (HostKeyFingerprint of the key the
// transport actually received) against the store's pinned value for host,
// falling back to explicitFingerprint (the --host-key-fingerprint flag
// value, "" if not supplied) as an out-of-band confirmation.
//
// Fail-closed decision table:
//   - no pinned record AND explicitFingerprint matches presented: accept,
//     and the caller (Pin) should then persist it.
//   - no pinned record AND no match: ErrHostKeyUnknown.
//   - pinned record matches presented: accept.
//   - pinned record does NOT match presented, even if explicitFingerprint
//     happens to equal presented: ErrHostKeyChanged. A changed key on a
//     previously-pinned host is never silently accepted by the same flag
//     that bootstraps a first pin — re-pinning a changed key is a
//     separate, explicit re-pin operation (Pin with force=true), not a
//     side effect of Verify.
func (k *KnownHosts) Verify(host, presentedFingerprint, explicitFingerprint string) error {
	pinned, err := k.backend.Load()
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "nodes: read known_hosts store")
	}
	existing, known := pinned[host]
	switch {
	case !known:
		if explicitFingerprint != "" && explicitFingerprint == presentedFingerprint {
			return nil
		}
		return ErrHostKeyUnknown(host)
	case existing == presentedFingerprint:
		return nil
	default:
		return ErrHostKeyChanged(host)
	}
}

// Pin records host's fingerprint as trusted. Called after a successful
// Verify on a first-seen host (fingerprint came from the matching
// explicitFingerprint path), or explicitly by an operator re-pinning a
// changed key with force=true after their own out-of-band confirmation.
// Pin never re-pins a changed key silently: force must be true whenever a
// pinned record for host already exists, or Pin refuses with
// ErrHostKeyChanged.
func (k *KnownHosts) Pin(host, fingerprint string, force bool) error {
	pinned, err := k.backend.Load()
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "nodes: read known_hosts store")
	}
	if existing, known := pinned[host]; known && existing != fingerprint && !force {
		return ErrHostKeyChanged(host)
	}
	pinned[host] = fingerprint
	if err := k.backend.Save(pinned); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "nodes: write known_hosts store")
	}
	return nil
}

// fileKnownHostsBackend is the production KnownHostsBackend: one JSON file
// under dataDir/nodes/known_hosts.json.
type fileKnownHostsBackend struct {
	path string
}

// NewFileKnownHostsBackend returns a KnownHostsBackend persisting at
// <dataDir>/nodes/known_hosts.json.
func NewFileKnownHostsBackend(dataDir string) KnownHostsBackend {
	return fileKnownHostsBackend{path: filepath.Join(dataDir, "nodes", "known_hosts.json")}
}

func (b fileKnownHostsBackend) Load() (map[string]string, error) {
	data, err := os.ReadFile(b.path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]string{}, nil
		}
		return nil, err
	}
	var m map[string]string
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, cascade.Wrap(cascade.KindIntegrity, err, "nodes: known_hosts store is not valid JSON")
	}
	if m == nil {
		m = map[string]string{}
	}
	return m, nil
}

func (b fileKnownHostsBackend) Save(m map[string]string) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return runtime.WriteBytesAtomic(b.path, data)
}
