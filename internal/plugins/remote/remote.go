// Package remote implements the P1-E15-W4-S33-T4 remote plugin runtime's
// connect-and-negotiate handshake: given a manifest declaring a remote
// runtime host, dial it over HTTP/1.1 POST /rpc carrying JSON-RPC 2.0 (an
// exact mirror of the unix-socket IPC shape, no custom framing, R-14.49),
// negotiate ABI versions, and refuse every actual dispatch call with
// ErrRemoteRuntimeDeferred: the full remote execution engine is out of
// scope for P1 and ships in P2.
//
// BOUNDARY NOTE (LANE-RULES §1, recorded because it overturns three of
// this ticket's own task lines): .golangci.yml's plugins-providers-
// boundary depguard rule denies every NON-TEST file matching
// "**/plugins/**/*.go" from importing internal/** at all — and unlike
// internal/plugins/dispatch.go (a file directly at internal/plugins/,
// which a documented rule-glob gap exempts), this package sits one
// directory further down, the same shape as internal/plugins/process and
// internal/plugins/wasm, which the tree already keeps internal-import-
// free. So this file, and egress.go, cannot import "net/http" wrapped
// egress control, "internal/hooks/egress", "internal/secrets", or
// "internal/build" the way the ticket's task list literally asks
// (calling egress.RegisterClass from a package-init here, or reading
// [plugins].enable_remote_runtime out of internal/runtime directly).
// Every place this file needs one of those, it takes it as an injected
// parameter or interface instead (enableRemoteRuntime bool, the
// Interceptor seam in egress.go) and the real internal/hooks/egress
// wiring lives in internal/plugins/dispatch.go, the one file in this
// tree the boundary exempts and the ticket's own files_scope already
// lists as a file this ticket changes.
//
// Inputs: a RemoteRuntimeConfig (manifest-declared host/port, this
// build's ABI version, a handshake timeout) and an Interceptor the
// caller has already obtained a real egress.Capability for.
// Outputs: a typed *cascade.Error on every non-success path (version
// mismatch, connection refusal, timeout, disabled-by-config); no bare
// error, no panic, no silent default-allow.
// Constraints: no credential ever reaches this package — it has no
// import path to internal/secrets at all (see BOUNDARY NOTE), which is
// the real, structural proof for this ticket's §5.21 requirement, not a
// runtime check that could be bypassed.
// SPORT: internal/plugins/remote (ADD) — P1-E15-W4-S33-T4.
package remote

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// HostABIVersionV1 is the plugin ABI version this build's remote-runtime
// handshake negotiates. A per-runtime constant, deliberately separate
// from wasm.HostABIVersionV1: the two runtimes may diverge in the future,
// and this package cannot import internal/plugins/wasm to share one
// (BOUNDARY NOTE, package doc).
const HostABIVersionV1 = 1

// DefaultHandshakeTimeout bounds one handshake round trip when
// RemoteRuntimeConfig.Timeout is zero.
const DefaultHandshakeTimeout = 5 * time.Second

// handshakePath is the fixed request path R-14.49 names: "HTTP/1.1 POST
// /rpc JSON-RPC 2.0 over TCP".
const handshakePath = "/rpc"

// RemoteRuntimeConfig is a remote-runtime manifest's resolved connection
// target: the [remote] table a RuntimeRemote manifest declares
// (pkg/plugin.RemoteSpec), plus this build's own ABI version and a
// handshake timeout.
//
//nolint:revive // stutters as remote.RemoteRuntimeConfig; this exact name is the ticket's own contract text (P1-E15-W4-S33-T4 task 1) and the "remote runtime config" concept genuinely needs both words to stay unambiguous next to RemoteSpec (pkg/plugin) and RuntimeMode (pkg/plugin).
type RemoteRuntimeConfig struct {
	// Host is the remote runtime's hostname or IP literal.
	Host string
	// Port is the remote runtime's TCP port.
	Port int
	// ABIVersion is the ABI version THIS host expects the remote to
	// agree to. Production callers pass HostABIVersionV1; tests may pass
	// another value to exercise the mismatch path.
	ABIVersion int
	// Timeout bounds the handshake round trip. Zero uses
	// DefaultHandshakeTimeout.
	Timeout time.Duration
}

// endpoint renders cfg's dial target.
func (cfg RemoteRuntimeConfig) endpoint() string {
	return fmt.Sprintf("http://%s:%d%s", cfg.Host, cfg.Port, handshakePath)
}

// validate reports whether cfg has enough to dial at all. This is a
// second, defence-in-depth check: pkg/plugin.Validate (validate.go)
// already refuses an empty remote.host at manifest-parse time, but
// dialRemote is also reachable directly (e.g. from a future retry path)
// without a manifest in hand, so it must not trust a zero Host/Port
// silently.
func (cfg RemoteRuntimeConfig) validate() error {
	if strings.TrimSpace(cfg.Host) == "" {
		return cascade.New(cascade.KindInvalidInput, "plugins/remote: config has no host to dial")
	}
	if cfg.Port <= 0 || cfg.Port > 65535 {
		return cascade.Newf(cascade.KindInvalidInput, "plugins/remote: port %d is not a valid TCP port", cfg.Port)
	}
	return nil
}

// timeout resolves cfg's effective handshake timeout.
func (cfg RemoteRuntimeConfig) timeout() time.Duration {
	if cfg.Timeout > 0 {
		return cfg.Timeout
	}
	return DefaultHandshakeTimeout
}

// ErrRemoteRuntimeNotEnabled is the Art.1.3 deferred-capability refusal:
// [plugins].enable_remote_runtime is false (the shipped default), so a
// remote-runtime manifest is refused with this structured, non-fatal
// warning rather than attempting a handshake. A caller (cmd/cascade's
// `plugin add` presentation layer) prints this as a warning, not a fatal
// error — the same shape ErrDaemonRequiredForElevatedPluginOp already
// establishes in internal/plugins/lifecycle_add.go for a different
// elevated-verb refusal.
func ErrRemoteRuntimeNotEnabled(pluginID string) error {
	return cascade.Newf(cascade.KindUnsupported,
		"plugin: %s: remote runtime not yet available; full support in P2. "+
			"Set [plugins].enable_remote_runtime = true to attempt the handshake (handshake only; "+
			"dispatch stays deferred).", pluginID)
}

// ErrRemoteRuntimeDeferred is returned by every remote-runtime dispatch
// call once the handshake succeeds: the full remote execution engine
// (dispatching plugin host-fn calls to a remote host) ships in P2.
func ErrRemoteRuntimeDeferred(pluginID string) error {
	return cascade.Newf(cascade.KindUnsupported,
		"plugin: %s: remote runtime handshake succeeded, but dispatch is deferred to P2 "+
			"(this build implements the handshake only)", pluginID)
}

// Dispatch is the composition root's single entry point for a remote-
// runtime manifest (called from internal/plugins/dispatch.go's
// ProvisionElevated): the config-guard + handshake + deferred-refusal
// sequence the ticket's task list describes as "the dispatch entry for
// remote-type plugins". It never returns a nil error: the false-flag
// path returns ErrRemoteRuntimeNotEnabled, a handshake failure returns
// its own typed error, and a handshake success still returns
// ErrRemoteRuntimeDeferred — there is no success return for this ticket,
// by design (Art.1: a stub must refuse by name, never fabricate an OK).
func Dispatch(ctx context.Context, cfg RemoteRuntimeConfig, enableRemoteRuntime bool, pluginID string, interceptor Interceptor) error {
	return dispatchWithDoer(ctx, cfg, enableRemoteRuntime, pluginID, interceptor, nil)
}

// dispatchWithDoer is Dispatch's real body, taking an injectable doer so
// this package's own _test.go files can exercise the enabled-handshake
// branch without a real socket (a nil d, Dispatch's only caller-visible
// path, uses the production realDoer via dialRemote's own default).
func dispatchWithDoer(
	ctx context.Context, cfg RemoteRuntimeConfig, enableRemoteRuntime bool, pluginID string, interceptor Interceptor, d doer,
) error {
	if !enableRemoteRuntime {
		return ErrRemoteRuntimeNotEnabled(pluginID)
	}
	if _, err := dialRemote(ctx, cfg, interceptor, d); err != nil {
		return err
	}
	return ErrRemoteRuntimeDeferred(pluginID)
}

// remoteConn is the handshake's successful outcome: proof the remote
// agreed to the negotiated ABI version. It carries no live connection —
// dialRemote's HTTP round trip is already complete by the time this is
// constructed — because there is nothing further to do with one until
// P2's execution engine exists.
type remoteConn struct {
	abiVersion int
}

// dialRemote performs the R-14.49 handshake: build the JSON-RPC request,
// pass it through interceptor (the caller's real egress.Capability-backed
// firewall), POST it to cfg.endpoint() via d, and negotiate ABI versions
// on the response. Every failure path returns a typed *cascade.Error
// naming which of version-mismatch, connection-refused, or timeout
// occurred.
//
// d is a doer (doer.go); a nil d uses the production realDoer. Callers
// outside this package always get realDoer — d is exposed only because
// dialRemote is unexported and this package's own _test.go files inject
// a fake one, keeping every real "net"/"net/http" round trip (and thus
// that import) out of every test file (TestNoNetworkUnitTest_RealTreeGreen,
// internal/build's tree-wide no-network-unit-lane gate — see doer.go's
// header comment for the violation this resolves).
func dialRemote(ctx context.Context, cfg RemoteRuntimeConfig, interceptor Interceptor, d doer) (*remoteConn, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return nil, cascade.New(cascade.KindUnavailable,
			"plugins/remote: no egress interceptor configured; refusing rather than sending the handshake unfiltered")
	}
	if d == nil {
		d = realDoer{}
	}

	body, err := encodeHandshakeRequest(cfg)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindInternal, err, "plugins/remote: encode handshake request")
	}
	body, err = interceptor.Intercept(ctx, body)
	if err != nil {
		return nil, err
	}

	reqCtx, cancel := context.WithTimeout(ctx, cfg.timeout())
	defer cancel()
	resp, err := d.Do(reqCtx, doRequest{URL: cfg.endpoint(), Body: body})
	if err != nil {
		return nil, classifyDialErr(reqCtx, err)
	}

	result, err := decodeHandshakeResponse(resp.Body)
	if err != nil {
		return nil, err
	}
	if result.ABIVersion != cfg.ABIVersion {
		return nil, cascade.Newf(cascade.KindConflict,
			"plugins/remote: remote reports ABI version %d, this host requires %d",
			result.ABIVersion, cfg.ABIVersion)
	}
	return &remoteConn{abiVersion: result.ABIVersion}, nil
}

// classifyDialErr maps a failed http.Client.Do into the named typed
// error the acceptance criteria require: KindTimeout when reqCtx's own
// deadline is what ended the call, KindUnavailable (naming "connection
// refused") for every other network-layer failure (a dial error, a
// reset, a refusal — Go does not expose "refused" as a distinct type
// from other dial failures in a platform-portable way, so this package
// reports the whole class as unavailable rather than guessing narrower).
func classifyDialErr(reqCtx context.Context, err error) error {
	if errors.Is(reqCtx.Err(), context.DeadlineExceeded) {
		return cascade.Wrap(cascade.KindTimeout, err, "plugins/remote: handshake timed out")
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return cascade.Wrap(cascade.KindUnavailable, err, "plugins/remote: remote unreachable (connection refused)")
	}
	return cascade.Wrap(cascade.KindUnavailable, err, "plugins/remote: handshake request failed")
}
