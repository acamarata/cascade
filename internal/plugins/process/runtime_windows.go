//go:build windows

// Purpose: the windows-tier ProcessRuntime.Launch: process plugins are
//
//	daemon-only, and the daemon is tier-2 on windows (06-FORGE-SPEC §2),
//	so Launch refuses with an actionable, typed error instead of
//	spawning (Art.5 platform parity).
//
// Inputs: a Manifest (unused beyond the refusal message).
// Outputs: always a typed ErrPlatformNotSupported; never a *Handle.
// Constraints: this file and its test carry the SAME //go:build windows
//
//	tag as the package's non-windows Launch (runtime.go), so exactly one
//	definition of ProcessRuntime.Launch exists per platform build.
//
// SPORT: internal/plugins/process runtime windows (ADD) — P1-E15-W4-S31-T3.

package process

import (
	"context"
	"io"
	"runtime"
	"sync"
	"time"
)

// Clock abstracts time.Now, mirroring the non-windows build's interface
// so ProcessRuntime's field shape is identical on every platform.
type Clock interface {
	Now() time.Time
}

// ProcessRuntime is the windows-tier stand-in: the field shape matches
// the non-windows build so callers compile unmodified, but Launch always
// refuses.
type ProcessRuntime struct {
	Clock          Clock
	Stderr         io.Writer
	Registrar      EgressRegistrar
	Interceptor    EgressInterceptor
	Restart        RestartPolicy
	Audit          AuditSink
	StartupTimeout time.Duration

	registerOnce sync.Once
}

// NewProcessRuntime builds a windows-tier ProcessRuntime. Launch on the
// result always refuses; construction itself never does, so a caller
// probing platform support can still build a zero-cost value.
func NewProcessRuntime() *ProcessRuntime {
	return &ProcessRuntime{}
}

// Launch always returns ErrPlatformNotSupported: process plugins require
// the daemon, and the daemon is tier-2 on windows.
func (rt *ProcessRuntime) Launch(_ context.Context, _ Manifest) (*Handle, error) {
	return nil, wrapPlatformNotSupported(runtime.GOOS)
}
