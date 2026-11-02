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
	// Actions is the node's durable action log (S-37.T2). Nil leaves the
	// dispatch leg unmounted rather than mounting one that cannot dedup.
	Actions ActionLog
	// EnrollmentID binds this node's result frames to its enrollment.
	EnrollmentID string
	// Elevation is the elevation middleware mounted on the registry. Nil
	// installs a refuse-all middleware (serve_registry.go): node serve is a
	// separate process that holds no custody session and cannot share the
	// daemon's ledger, trust or audit log, so an elevated verb (node.enroll
	// among them) is refused on this surface. Local `cascade node enroll` is
	// unchanged.
	Elevation rpc.MiddlewareFunc
	// AttestSigner builds and signs the attestation for one ci.attest.sign
	// request (contract:node-attest-sign). Nil leaves ci.attest.sign
	// unmounted. It is reached only after the request verified against the
	// controller key ControllerBinding pins.
	AttestSigner AttestSigner
	// ControllerBinding is where the pinned controller key is read from, per
	// request. Nil (or a binding without a key) refuses every ci.attest.sign.
	ControllerBinding ControllerBindingBackend
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
// RPC surface. ci.attest.sign is mounted here, and only when
// deps.AttestSigner is non-nil.
func NewServeRegistry(deps ServeDeps) *ServeRegistry {
	reg := BuildServeRegistry(deps)
	if deps.AttestSigner != nil {
		reg.Register(AttestSignMethod, attestSignHandler(attestSignDeps{
			binding: deps.ControllerBinding, clock: deps.Clock, nodeID: deps.Self.NodeID, signer: deps.AttestSigner,
		}))
	}
	return &ServeRegistry{reg: reg}
}

// Call dispatches one method through the registry in process, with the same
// middleware chain a socket request takes, and decodes the result into out.
// Its shape is the call seam ci.RequestNodeSignature takes. A handler error
// comes back as a cascade error of the same Kind.
func (r *ServeRegistry) Call(ctx context.Context, method string, params, out any) error {
	raw, err := json.Marshal(params)
	if err != nil {
		return cascade.Wrap(cascade.KindInvalidInput, err, "nodes: encode call params")
	}
	result, eo := r.reg.Dispatch(ctx, &rpc.Request{JSONRPC: "2.0", Method: method, Params: raw})
	if eo != nil {
		kind, ok := cascade.KindFromJSONRPCCode(eo.Code)
		if !ok {
			kind = cascade.KindInternal
			if strings.HasPrefix(eo.Message, "method not found") {
				kind = cascade.KindNotFound
			}
		}
		return cascade.New(kind, eo.Message)
	}
	if out == nil {
		return nil
	}
	body, err := json.Marshal(result)
	if err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "nodes: encode call result")
	}
	if err := json.Unmarshal(body, out); err != nil {
		return cascade.Wrap(cascade.KindIntegrity, err, "nodes: decode call result")
	}
	return nil
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
