//go:build !windows

// Purpose: separates a plugin process's LIFETIME from the deadline that
//
//	bounds its startup handshake, and spawns the Commander against the
//	former.
//
// Inputs: the lifetime context a Launch caller owns, and a Manifest.
//
// Outputs: a started Commander plus the Transport wrapping its stdio.
//
// Constraints: the context handed to the CommandFactory becomes the
//
//	child process's kill switch — defaultCommandFactory builds an
//	os/exec.CommandContext, which SIGKILLs the child the moment that
//	context is done. Conflating it with the startup deadline is therefore
//	not a timeout bug but a lifetime bug: Launch's `defer cancel()` fires
//	as soon as Launch returns, so every plugin was killed immediately on
//	launch, before it could issue a single host call. The conformance
//	suite (internal/plugins/conformance) only ever passed by winning the
//	race between the shell's second write and Launch's return; under
//	whole-tree `go test ./...` load the Go side wins and every fixture
//	that needs a real host-boundary observation times out. spawnOne takes
//	only the lifetime context for exactly this reason; performHandshake
//	keeps the bounded one.
//
// SPORT: internal/plugins/process lifetime (ADD) — P1-E15-W4-S31-T3.

package process

import (
	"context"

	"github.com/acamarata/cascade/pkg/cascade"
)

// spawnOne builds a Commander from manifest, opens its stdio pipes,
// starts it, and wraps its stdio in a Transport. lifetimeCtx governs how
// long the child may run and MUST NOT carry the startup deadline: see
// this file's Constraints.
func (rt *ProcessRuntime) spawnOne(lifetimeCtx context.Context, manifest Manifest) (Commander, *Transport, error) {
	cmd := rt.factory()(lifetimeCtx, manifest)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, cascade.Wrap(cascade.KindUnavailable, err, "process: opening plugin stdin")
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, cascade.Wrap(cascade.KindUnavailable, err, "process: opening plugin stdout")
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, cascade.Wrap(cascade.KindUnavailable, err, "process: starting plugin process")
	}
	return cmd, NewTransport(stdin, stdout, rt.StartupTimeout), nil
}
