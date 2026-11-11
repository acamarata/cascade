package opencode

// Purpose: synchronized job observations and output custody.
// Inputs: filtered JSON events and process exit. Outputs: read-only job views.
// Constraints: failed jobs withhold output; callers never alias native buffers.

import (
	"context"
	"os"
	"strings"
	"sync"

	"github.com/acamarata/cascade/pkg/provider"
)

type job struct {
	id             provider.AgentJobID
	spec           provider.AgentJobSpec
	tier           provider.DataClass
	process        *os.Process
	stdout, stderr *os.File
	pgid           int
	done           chan struct{}
	mu             sync.Mutex
	state          provider.AgentRunState
	err            error
	output         strings.Builder
	events         []provider.AgentEvent
	norm           *provider.EventNormalizer
	seq            uint64
	session        string
	terminal       bool
	stopping       bool
	closed         bool
	filterMu       sync.Mutex
}

// Status returns the synchronized lifecycle state.
func (d *Driver) Status(_ context.Context, id provider.AgentJobID) (provider.AgentRunState, error) {
	j, err := d.lookup(id)
	if err != nil {
		return "", err
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.state, nil
}

// Collect waits for confirmed exit and withholds content on failure.
func (d *Driver) Collect(ctx context.Context, id provider.AgentJobID) (provider.CollectResult, error) {
	j, err := d.lookup(id)
	if err != nil {
		return provider.CollectResult{}, err
	}
	select {
	case <-j.done:
	case <-ctx.Done():
		return provider.CollectResult{}, ErrRunCancelled
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	r := provider.CollectResult{JobID: id, Artifacts: []string{}, DataClass: j.tier, SessionID: j.session}
	if j.err == nil {
		r.Output = j.output.String()
	}
	return r, j.err
}

// Artifacts returns a non-nil empty list; changes remain in the leased tree.
func (d *Driver) Artifacts(_ context.Context, id provider.AgentJobID) ([]string, error) {
	if _, err := d.lookup(id); err != nil {
		return nil, err
	}
	return []string{}, nil
}

// Events returns independent copies of the filtered native event log.
func (d *Driver) Events(id provider.AgentJobID) ([]provider.AgentEvent, error) {
	j, err := d.lookup(id)
	if err != nil {
		return nil, err
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	out := append([]provider.AgentEvent{}, j.events...)
	for i := range out {
		if n := out[i].Native; n != nil {
			out[i].Native = &provider.NativeEvent{Type: n.Type, Raw: append([]byte(nil), n.Raw...)}
		}
	}
	return out, nil
}

func (j *job) record(raw []byte) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return ErrRunCancelled
	}
	j.seq++
	e, session, err := decodeEvent(raw, j.seq, j.tier)
	if err != nil {
		return err
	}
	ev, keep, err := j.norm.Normalize(e)
	if err != nil || !keep {
		return err
	}
	ev.Protocol = ProtocolRevision
	j.events = append(j.events, ev)
	j.output.WriteString(ev.Text)
	if session != "" {
		j.session = session
	}
	if ev.Kind == provider.DriverEventDone {
		j.terminal = true
	}
	return nil
}
