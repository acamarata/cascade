// Purpose: the localllm sidecar seam (R-14.36): the interface plus the IPC
//   handshake spec a future post-P1 native local-LLM sidecar binary must
//   implement. This is NOT the ollama driver (providers/ollama), which
//   speaks to an already-running Ollama HTTP server directly; this seam is
//   for a daemon-launched sidecar SUBPROCESS speaking a fixed transport.
// Inputs: none at this layer - contract only.
// Outputs: none.
// Constraints: fixed transport (R-14.36, mirroring 06-FORGE-SPEC.md §2's
//   IPC shape) - a unix domain socket carrying HTTP/1.1, JSON-RPC 2.0 POST
//   requests on LocalLLMSidecarRPCPath, and a plain liveness probe on
//   LocalLLMSidecarHealthPath. Lifecycle is exactly start/stop/health.
//   Credentials are never forwarded across this boundary - the sidecar
//   manages its own local authentication, if any. pkg/provider imports
//   nothing from internal/ (Art.10.2). Art.1.3: no implementation of this
//   interface ships in P1 - the sidecar binary is an explicit post-P1
//   artifact (04-PEWS-PLAN-W1-W3.md §Wave 3 §Epic J S-19.T5 DECIDED). The
//   seam itself is complete; what is absent is the external binary, not
//   cascade code, so no // CASCADE-ALLOW marker applies here.
// SPORT: pkg.provider.localllm-sidecar-seam/ADD (P1-E10-W3-S19-T5).

package provider

import "context"

// LocalLLMSidecarRPCPath is the fixed HTTP path a sidecar's unix-socket
// listener serves JSON-RPC 2.0 POST requests on (R-14.36, mirroring
// 06-FORGE-SPEC.md §2's IPC shape).
const LocalLLMSidecarRPCPath = "/rpc"

// LocalLLMSidecarHealthPath is the fixed HTTP path a sidecar's unix-socket
// listener serves a GET liveness probe on (R-14.36).
const LocalLLMSidecarHealthPath = "/health"

// LocalLLMSidecarHealth is the result of a GET LocalLLMSidecarHealthPath
// probe against a running sidecar.
type LocalLLMSidecarHealth struct {
	// Ready reports whether the sidecar is ready to accept
	// LocalLLMSidecarRPCPath calls.
	Ready bool `json:"ready"`
	// Detail carries an optional human-readable status message (e.g. a
	// model still loading, or a startup error the sidecar wants surfaced).
	Detail string `json:"detail,omitempty"`
}

// LocalLLMSidecarProvider is the contract a post-P1 native local-LLM
// sidecar binary implements against the daemon's subprocess-lifecycle
// manager (R-14.36). The daemon invokes the sidecar as a subprocess; once
// running, the sidecar listens on a unix domain socket speaking HTTP/1.1,
// exposing LocalLLMSidecarRPCPath (JSON-RPC 2.0 POST, mirroring
// 06-FORGE-SPEC.md §2's IPC shape) and LocalLLMSidecarHealthPath (GET
// liveness). Credentials are never forwarded across this boundary - the
// sidecar manages its own local authentication, if any (R-14.36); no
// method here accepts or returns a credential value.
//
// Art.1.3: no implementation of this interface ships in P1. The sidecar
// binary is an explicit post-P1 artifact (04-PEWS-PLAN-W1-W3.md §Wave 3
// §Epic J S-19.T5 DECIDED: "native local-llm sidecar ships as separate
// post-P1 artifact; seam integration-tested when present"); this seam
// compiles and is exported so a future implementation has a fixed target,
// but nothing in this phase constructs or registers one. This exported
// name is fixed by the contract; no alternative or analogous interface
// name is introduced for the same concept.
type LocalLLMSidecarProvider interface {
	// Start launches the sidecar subprocess so it will listen on a unix
	// domain socket at socketPath, and blocks until that socket is
	// accepting connections, ctx is done, or the subprocess fails to
	// become ready.
	Start(ctx context.Context, socketPath string) error
	// Stop terminates the sidecar subprocess. It is safe to call Stop on a
	// sidecar that was never started or has already stopped.
	Stop(ctx context.Context) error
	// Health probes the sidecar's GET LocalLLMSidecarHealthPath endpoint
	// and reports its current liveness state.
	Health(ctx context.Context) (LocalLLMSidecarHealth, error)
}
