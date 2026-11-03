package codex

// Purpose: one job's record (state, output, events, journal fields) and
// the read-side provider methods: Status, Collect, Artifacts, Events,
// Journal.
// Inputs: the run loop's observations.
// Outputs: provider views of the job, every one carrying the job's tier.
// Constraints: Collect blocks until the child exited; a failed job's
// output is withheld.
// SPORT: providers.agents.codex/ADD (P1-AGT-05).

import (
	"context"
	"io"
	"os/exec"
	"strings"
	"sync"

	"github.com/acamarata/cascade/pkg/provider"
)

// Journal holds the per-session fields a journal records.
type Journal struct {
	ThreadID       string // thread/start result thread.id (resume key)
	TurnID         string
	UserAgent      string // InitializeResponse.userAgent
	CodexVersion   string // parsed from UserAgent
	SchemaRevision string // ProtocolRevision + "/" + codex version
}

// job is one spawned App Server child.
type job struct {
	id       provider.AgentJobID
	tier     provider.DataClass
	filter   provider.OutputFilter
	codexDir string // expected InitializeResponse.codexHome
	spec     provider.AgentJobSpec
	sandbox  string
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	pgid     int
	reader   *lineReader
	done     chan struct{}

	wmu    sync.Mutex
	closed bool

	mu          sync.Mutex
	state       provider.AgentRunState
	err         error
	output      strings.Builder
	events      []provider.AgentEvent
	norm        *provider.EventNormalizer
	seq         uint64
	nextID      int64
	outstanding map[string]bool
	journal     Journal
	turnStatus  string
	stopping    bool
}

func newJob(id provider.AgentJobID, spec provider.AgentJobSpec, codexDir string) *job {
	return &job{
		id:          id,
		tier:        provider.JoinSensitivity(spec.DataClass, spec.Envelope.Sensitivity),
		filter:      spec.Filter,
		codexDir:    codexDir,
		spec:        spec,
		done:        make(chan struct{}),
		state:       provider.AgentRunRunning,
		norm:        provider.NewEventNormalizer(),
		outstanding: make(map[string]bool),
	}
}

// write sends one line to the child; after closeStdin it is a no-op.
func (j *job) write(line []byte) error {
	j.wmu.Lock()
	defer j.wmu.Unlock()
	if j.closed {
		return nil
	}
	_, err := j.stdin.Write(line)
	return err
}

func (j *job) closeStdin() {
	j.wmu.Lock()
	defer j.wmu.Unlock()
	if !j.closed {
		j.closed = true
		_ = j.stdin.Close()
	}
}

// record normalizes one event and appends it; a regressing Seq returns
// provider.ErrOutOfOrderEvent and a repeated DedupKey is dropped.
func (j *job) record(ev provider.DriverEvent) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.recordLocked(ev)
}

func (j *job) recordLocked(ev provider.DriverEvent) error {
	ev.DataClass = provider.JoinSensitivity(ev.DataClass, j.tier)
	out, keep, err := j.norm.Normalize(ev)
	if err != nil || !keep {
		return err
	}
	out.Protocol = ProtocolRevision
	j.events = append(j.events, out)
	return nil
}

// native records a frame as a NativeEvent with the next Seq.
func (j *job) native(kind provider.DriverEventKind, method, dedup string, filtered []byte) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.seq++
	return j.recordLocked(provider.DriverEvent{
		Kind: kind, Seq: j.seq, DedupKey: dedup, DataClass: j.tier,
		Native: &provider.NativeEvent{Type: method, Raw: filtered},
	})
}

// Status reports the job's state.
func (d *Driver) Status(_ context.Context, id provider.AgentJobID) (provider.AgentRunState, error) {
	j, err := d.lookup(id)
	if err != nil {
		return "", err
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.state, nil
}

// Collect blocks until the child exited and returns the filtered agent
// output, the thread id as SessionID and the job's tier. A job that ended
// in error returns that typed error and withholds its output.
func (d *Driver) Collect(ctx context.Context, id provider.AgentJobID) (provider.CollectResult, error) {
	j, err := d.lookup(id)
	if err != nil {
		return provider.CollectResult{}, err
	}
	select {
	case <-j.done:
	case <-ctx.Done():
		return provider.CollectResult{}, ctx.Err()
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	res := provider.CollectResult{JobID: id, Artifacts: []string{}, DataClass: j.tier, SessionID: j.journal.ThreadID}
	if j.err != nil {
		return res, j.err
	}
	res.Output = j.output.String()
	return res, nil
}

// Artifacts returns an empty, non-nil list: changes stay in the worktree.
func (d *Driver) Artifacts(_ context.Context, id provider.AgentJobID) ([]string, error) {
	if _, err := d.lookup(id); err != nil {
		return nil, err
	}
	return []string{}, nil
}

// Events returns a copy of the job's ordered event log so far.
func (d *Driver) Events(id provider.AgentJobID) ([]provider.AgentEvent, error) {
	j, err := d.lookup(id)
	if err != nil {
		return nil, err
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	return append([]provider.AgentEvent(nil), j.events...), nil
}

// Journal returns the job's session journal fields.
func (d *Driver) Journal(id provider.AgentJobID) (Journal, error) {
	j, err := d.lookup(id)
	if err != nil {
		return Journal{}, err
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.journal, nil
}

// ProcessGroup returns the job's process group id.
func (d *Driver) ProcessGroup(id provider.AgentJobID) (int, error) {
	j, err := d.lookup(id)
	if err != nil {
		return 0, err
	}
	return j.pgid, nil
}
