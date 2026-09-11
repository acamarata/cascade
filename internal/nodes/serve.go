// Purpose: `cascade node serve`'s platform-independent composition: the
//
//	node agent's own local identity bootstrap, the RPC registry mounting
//	node.enroll (S-36.T1's RegisterHandlers) and node.heartbeat (this
//	ticket's RegisterHeartbeatHandler) on the node's own unix-socket
//	endpoint, and the Windows tier-2 refusal every verb shares.
//
// Inputs: a ServeDeps built by the CLI composition root
//
//	(cmd/cascade/node_serve.go, out of this file's own concerns — this
//	file is domain logic, not cobra wiring).
//
// Outputs: an *rpc.Registry ready to be served over the node's socket, or
//
//	a typed fail-closed error.
//
// Constraints: 06-FORGE-SPEC §2 — Windows tier-2 is daemon-class outside
//
//	the binary + headless one-shot promise: `cascade node serve` refuses
//	unconditionally on Windows. cmd/cascade/lifecycle_windows.go's
//	established pattern is a SEPARATE build-tagged file per platform; this
//	ticket's files_scope allows exactly one cmd/cascade file
//	(node_serve.go), so the refusal is a runtime GOOS check here instead
//	of a structural per-file split — see the ticket journal's
//	CONTRADICTIONS section for the full quote of both sides. RefuseOnGOOS
//	is unit-tested directly against the literal "windows" input so the
//	refusal path is asserted without needing a windows build.
//
// SPORT: internal/nodes ServeDeps/ADDED, EnsureLocalIdentity/ADDED,
//
//	BuildServeRegistry/ADDED (P1-E17-W4-S36-T2).

package nodes

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// windowsTier2Hint is the actionable refusal message every Windows verb
// on this surface shares, mirroring internal/daemon/lifecycle_windows.go's
// windowsRefusalHint convention exactly.
const windowsTier2Hint = "cascade has no node-serve daemon on Windows (tier-2); " +
	"the serve listener is daemon-class, outside the binary + headless one-shot promise (06-FORGE-SPEC §2)"

// RefuseOnGOOS reports the typed tier-2 refusal for goos == "windows", nil
// otherwise. Called with runtime.GOOS by the CLI composition root
// (cmd/cascade/node_serve.go); exists as a pure function here so it is
// unit-testable against the literal string "windows" without a
// windows-tagged file or a GOOS-conditional build.
func RefuseOnGOOS(goos string) error {
	if goos == "windows" {
		return cascade.New(cascade.KindUnsupported, "node serve: "+windowsTier2Hint)
	}
	return nil
}

// SelfIdentity is the node agent's own identity record, persisted
// locally (not in RecordStore, which holds records for PEER nodes this
// process has enrolled/trusts — a node's own identity is not one of its
// own device records).
type SelfIdentity struct {
	NodeID    string `json:"node_id"`
	PubKeyB64 string `json:"pubkey_b64"`
}

// SelfIdentityBackend persists the node's own SelfIdentity. fileBackend
// (below) is the production implementation; tests use an in-memory fake
// or a t.TempDir()-rooted file backend.
type SelfIdentityBackend interface {
	// Load returns the persisted identity and true, or a zero value and
	// false if none has been generated yet — "not yet bootstrapped" is a
	// valid state, not an error.
	Load() (SelfIdentity, bool, error)
	Save(SelfIdentity) error
}

// NewFileSelfIdentityBackend returns a SelfIdentityBackend persisting at
// <dataDir>/nodes/self_identity.json, mirroring
// NewFileRecordBackend/NewFileKnownHostsBackend's identical precedent.
func NewFileSelfIdentityBackend(dataDir string) SelfIdentityBackend {
	return fileSelfIdentityBackend{path: filepath.Join(dataDir, "nodes", "self_identity.json")}
}

type fileSelfIdentityBackend struct{ path string }

func (b fileSelfIdentityBackend) Load() (SelfIdentity, bool, error) {
	data, err := os.ReadFile(b.path)
	if err != nil {
		if os.IsNotExist(err) {
			return SelfIdentity{}, false, nil
		}
		return SelfIdentity{}, false, cascade.Wrap(cascade.KindUnavailable, err, "nodes: read self identity")
	}
	var id SelfIdentity
	if err := json.Unmarshal(data, &id); err != nil {
		return SelfIdentity{}, false, cascade.Wrap(cascade.KindIntegrity, err, "nodes: self identity file is not valid JSON")
	}
	return id, true, nil
}

func (b fileSelfIdentityBackend) Save(id SelfIdentity) error {
	data, err := json.MarshalIndent(id, "", "  ")
	if err != nil {
		return err
	}
	return runtime.WriteBytesAtomic(b.path, data)
}

// EnsureLocalIdentity returns this node's own identity, generating and
// persisting one (both the SelfIdentityBackend record and the private
// key in keystore, per R-21.220 custody) on first run. A second call
// against the same backend returns the SAME identity — first-run
// bootstrap is idempotent, never re-generating a live key.
func EnsureLocalIdentity(ctx context.Context, backend SelfIdentityBackend, keystore *NodeKeystore, rnd io.Reader) (Identity, error) {
	if existing, ok, err := backend.Load(); err != nil {
		return Identity{}, err
	} else if ok {
		return ValidateIdentity(existing.NodeID, existing.PubKeyB64)
	}
	if rnd == nil {
		rnd = rand.Reader
	}
	identity, priv, err := GenerateIdentity(rnd)
	if err != nil {
		return Identity{}, err
	}
	if err := keystore.Store(ctx, identity.NodeID, priv); err != nil {
		return Identity{}, err
	}
	if err := backend.Save(SelfIdentity{NodeID: identity.NodeID, PubKeyB64: identity.PubKeyB64()}); err != nil {
		return Identity{}, err
	}
	return identity, nil
}

// ServeDeps carries every collaborator the node agent's RPC surface
// needs. The CLI composition root (cmd/cascade/node_serve.go) constructs
// one from the real environment; tests construct one over t.TempDir()
// backends.
type ServeDeps struct {
	Records      *RecordStore
	KnownHosts   *KnownHosts
	Keystore     *NodeKeystore
	Self         Identity
	Precondition runtime.ElevationPrecondition
	Sequences    *SequenceStore
	Clock        Clock
	Timeout      time.Duration
}

// BuildServeRegistry mounts node.enroll (S-36.T1's RegisterHandlers) and
// node.heartbeat (this ticket's RegisterHeartbeatHandler) on a fresh
// *rpc.Registry — the node agent's complete RPC surface as of this
// ticket. internal/nodes is an internal/rpc SERVER package (it is allowed
// to name rpc.Registry); the CLI composition root (cmd/cascade/node_serve.go)
// is a client of THIS package instead, through NewServeRegistry below,
// because cmd/cascade may never import internal/rpc directly (the
// cmd-rpc-server boundary, internal/client/boundary_test.go). serve_test.go
// drives Dispatch through the identical registry a real client would use,
// and proves the wiring is load-bearing by showing Dispatch fails with
// method-not-found when this function is never called.
func BuildServeRegistry(deps ServeDeps) *rpc.Registry {
	registry := rpc.NewRegistry()
	RegisterHandlers(registry, EnrollDeps{
		Records:             deps.Records,
		KnownHosts:          deps.KnownHosts,
		Keystore:            deps.Keystore,
		ControllerNodeID:    deps.Self.NodeID,
		ControllerPubKeyB64: deps.Self.PubKeyB64(),
	}, deps.Precondition)
	RegisterHeartbeatHandler(registry, HeartbeatDeps{
		Records:   deps.Records,
		Sequences: deps.Sequences,
		Clock:     deps.Clock,
		Timeout:   deps.Timeout,
	})
	return registry
}

// RPCPath re-exports internal/rpc.RPCPath's value as a plain string, so
// the CLI composition root's outbound heartbeat sender (which dials OUT
// to a controller's RPC route, an internal/client-shaped concern) can
// build the request URL without importing internal/rpc for a single
// constant.
const RPCPath = rpc.RPCPath

// ServeRegistry is an opaque handle around the node agent's *rpc.Registry:
// it lets the CLI composition root hold and serve the registry
// NewServeRegistry built without ever naming an internal/rpc type itself
// (the cmd-rpc-server boundary forbids cmd/cascade importing internal/rpc
// at all, not just constructing a Registry by hand — see BuildServeRegistry's
// doc comment).
type ServeRegistry struct {
	reg *rpc.Registry
}

// NewServeRegistry is BuildServeRegistry wrapped behind ServeRegistry —
// the CLI composition root's only allowed way to obtain the node agent's
// RPC surface.
func NewServeRegistry(deps ServeDeps) *ServeRegistry {
	return &ServeRegistry{reg: BuildServeRegistry(deps)}
}

// Handler returns an http.Handler dispatching through this registry, for
// mounting on an *http.Server the CLI composition root owns and serves
// (net/http is a stdlib boundary, not internal/rpc).
func (r *ServeRegistry) Handler() http.Handler {
	return rpc.NewHandler(r.reg)
}

// ConnContext is internal/rpc.ConnContext, re-exported so the CLI
// composition root can assign it to http.Server.ConnContext without
// importing internal/rpc. It is stateless (a pure per-connection context
// decorator), so it is exposed directly rather than as a ServeRegistry
// method.
var ConnContext = rpc.ConnContext

// ControllerBinding is the local record of this node's enrollment with a
// controller: the endpoint the heartbeat scheduler sends to, and the
// controller-issued enrollment id every frame embeds (heartbeat_sign.go's
// DeriveEnrollmentID, computed and returned BY the controller during
// enrollment; this node persists it verbatim rather than re-deriving it,
// since only the controller holds the DeviceRecord DeriveEnrollmentID is
// computed from).
type ControllerBinding struct {
	Endpoint     string `json:"endpoint"`
	EnrollmentID string `json:"enrollment_id"`
}

// ControllerBindingBackend persists ControllerBinding, captured at
// enrollment (08-INIT-CONFIG-SPEC §1 step 2: worker profile -> controller
// endpoint -> node enroll handoff). The WRITE side is S-36.T4's
// `node enroll` CLI verb, out of this ticket's files_scope; this ticket
// owns the READ side the heartbeat scheduler consumes, and an absent
// file (no enrollment has run yet on this node) is a valid "nothing to
// heartbeat to yet" state, never an error.
type ControllerBindingBackend interface {
	// Load returns the binding and true, or a zero value and false if
	// this node has not yet enrolled with a controller.
	Load() (ControllerBinding, bool, error)
}

// NewFileControllerBindingBackend returns a ControllerBindingBackend
// reading <dataDir>/nodes/controller_binding.json, mirroring
// NewFileSelfIdentityBackend's identical precedent.
func NewFileControllerBindingBackend(dataDir string) ControllerBindingBackend {
	return fileControllerBindingBackend{path: filepath.Join(dataDir, "nodes", "controller_binding.json")}
}

type fileControllerBindingBackend struct{ path string }

func (b fileControllerBindingBackend) Load() (ControllerBinding, bool, error) {
	data, err := os.ReadFile(b.path)
	if err != nil {
		if os.IsNotExist(err) {
			return ControllerBinding{}, false, nil
		}
		return ControllerBinding{}, false, cascade.Wrap(cascade.KindUnavailable, err, "nodes: read controller binding")
	}
	var binding ControllerBinding
	if err := json.Unmarshal(data, &binding); err != nil {
		return ControllerBinding{}, false, cascade.Wrap(cascade.KindIntegrity, err, "nodes: controller binding file is not valid JSON")
	}
	if strings.TrimSpace(binding.Endpoint) == "" {
		return ControllerBinding{}, false, nil
	}
	return binding, true, nil
}
