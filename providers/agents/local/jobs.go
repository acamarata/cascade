// Purpose: Driver's in-process AgentProvider job lifecycle - jobRecord,
//   mintID, lookup, Message/Status/Cancel/Collect/Artifacts, and the
//   protocol-negotiation pair SupportedProtocols/Negotiate - split out of
//   driver.go purely to stay under Art.10.3's 300-line/file cap once this
//   fix's ModelExecutor/seams.go split landed (same pattern
//   audit_helpers.go's own split-out note documents: behavior-preserving,
//   same package, no signature changes).
// Inputs: n/a beyond what each method's own doc comment already states.
// Outputs: n/a (see driver.go for Driver's overall Purpose/Inputs/Outputs).
// Constraints: same as driver.go (providers -> pkg only, Art.7.2; no bare
//   time.Now/math-rand).
// SPORT: providers.agents.local/CHANGE (file split only) —
//   FIX-manifest-collision-and-conductor-seam.

package local

import (
	"context"
	"fmt"

	"github.com/acamarata/cascade/pkg/provider"
)

// jobRecord is one Spawn'd job's in-process state. The local lane runs a
// job to completion synchronously inside Spawn, so every record is
// terminal the instant it exists.
type jobRecord struct {
	state     provider.AgentRunState
	output    string
	dataClass provider.DataClass
}

// mintID returns a freshly minted, unique AgentJobID from an in-process
// counter (never math/rand, never a bare clock read).
func (d *Driver) mintID() provider.AgentJobID {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.nextID++
	return provider.AgentJobID(fmt.Sprintf("local-%d", d.nextID))
}

// lookup returns id's job record, or ErrJobNotFound.
func (d *Driver) lookup(id provider.AgentJobID) (*jobRecord, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	rec, ok := d.jobs[id]
	if !ok {
		return nil, provider.ErrJobNotFound
	}
	return rec, nil
}

// Message delivers turn against an existing job. The local lane runs a
// job to completion inside Spawn, so a later Message is accepted (the
// job exists) but has no running turn left to affect; ErrJobNotFound
// covers an unknown id.
func (d *Driver) Message(_ context.Context, id provider.AgentJobID, _ string) error {
	_, err := d.lookup(id)
	return err
}

// Status returns id's current AgentRunState, or ErrJobNotFound.
func (d *Driver) Status(_ context.Context, id provider.AgentJobID) (provider.AgentRunState, error) {
	rec, err := d.lookup(id)
	if err != nil {
		return "", err
	}
	return rec.state, nil
}

// Cancel is a no-op success against an existing job: every local job is
// already terminal by the time Spawn returns, so there is nothing left
// to tear down. ErrJobNotFound covers an unknown id.
func (d *Driver) Cancel(_ context.Context, id provider.AgentJobID) error {
	_, err := d.lookup(id)
	return err
}

// Collect returns id's CollectResult immediately: every local job is
// already terminal by the time Spawn returns, so Collect never blocks.
// ErrJobNotFound covers an unknown id, including one never spawned.
func (d *Driver) Collect(_ context.Context, id provider.AgentJobID) (provider.CollectResult, error) {
	rec, err := d.lookup(id)
	if err != nil {
		return provider.CollectResult{}, err
	}
	return provider.CollectResult{JobID: id, Output: rec.output, Artifacts: []string{}, DataClass: rec.dataClass}, nil
}

// Artifacts always returns a non-nil, empty slice: the local lane writes
// no artifact files. ErrJobNotFound covers an unknown id.
func (d *Driver) Artifacts(_ context.Context, id provider.AgentJobID) ([]string, error) {
	if _, err := d.lookup(id); err != nil {
		return nil, err
	}
	return []string{}, nil
}

// localProtocolRange is this driver's declared ProtocolRange. It is a
// single-version range: the local lane speaks no vendor wire protocol, so
// there is exactly one version to negotiate.
var localProtocolRange = provider.ProtocolRange{Min: "local-v1", Max: "local-v1"}

// SupportedProtocols returns this driver's declared ProtocolRange.
func (d *Driver) SupportedProtocols() provider.ProtocolRange {
	return localProtocolRange
}

// Negotiate pins the one version in the intersection of this driver's
// range and peer, or ErrHarnessIncompatible when they do not intersect
// (R-21.158).
func (d *Driver) Negotiate(_ context.Context, peer provider.ProtocolRange) (provider.ProtocolVersion, error) {
	return provider.NegotiateProtocol(localProtocolRange, peer)
}
