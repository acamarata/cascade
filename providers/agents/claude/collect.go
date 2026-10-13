// Purpose: Collect and the one-time finalize that turns a finished job into
//   its terminal state and CollectResult: the revision-1 result object
//   (session_id, structured_output, total_cost_usd as a cli-observation
//   estimate), the closed journal rows, the artifact snapshot and every
//   fail-closed verdict (egress, overflow, leak, canary, a failed callback
//   channel, exit status, canary in artifact content).
//   Also Message (a capability probe: ErrMessageUnsupported), Status and the
//   explicit refusals for the model verbs and approvals.
// Inputs: the finished jobRecord (filtered stdout, frames, scanners).
// Outputs: provider.CollectResult or a typed error; the terminal state.
// Constraints: Output is the JSON of the closed JournalRow projection only
//   (R-21.152); stdout must be the single result object, decoded in memory
//   here (no internal/ import) and yielding the done row, usage and cost
//   but never a tool event: tool rows come only from authenticated callback
//   frames; DataClass joins the spec class with every row, never lower
//   (R-21.143); a failed verdict leaves no output in the result.
// SPORT: providers.agents.claude/ADD (P1-E30-W6-S61-T2).

package claude

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// ErrVendorAuthEnvLeak fails a run that left a process in its group
// carrying the vendor auth variable, or one whose environment could not be
// read to prove otherwise (R-21.151 process tree).
var ErrVendorAuthEnvLeak = cascade.New(cascade.KindPermissionDenied,
	"claude: a process outlived the harness with the vendor auth variable (or an unreadable environment)")

var errUnsupportedModelOp = cascade.New(cascade.KindUnsupported,
	"claude: the driver dispatches jobs; Chat/Embed/Count/Stream are not supported")

const finalizeGitTimeout = 30 * time.Second

// Chat refuses: see errUnsupportedModelOp.
func (d *Driver) Chat(context.Context, provider.ChatRequest) (provider.ChatResponse, error) {
	return provider.ChatResponse{}, errUnsupportedModelOp
}

// Embed refuses: see errUnsupportedModelOp.
func (d *Driver) Embed(context.Context, provider.ModelEmbedRequest) (provider.ModelEmbedResponse, error) {
	return provider.ModelEmbedResponse{}, errUnsupportedModelOp
}

// Count refuses: see errUnsupportedModelOp.
func (d *Driver) Count(context.Context, provider.CountRequest) (provider.CountResponse, error) {
	return provider.CountResponse{}, errUnsupportedModelOp
}

// Stream refuses: see errUnsupportedModelOp.
func (d *Driver) Stream(context.Context, provider.ChatRequest, provider.StreamSink) error {
	return errUnsupportedModelOp
}

// ApprovalRequests returns a closed channel: --permission-prompts none
// turns every prompt into a denial, so no approval is ever raised.
func (d *Driver) ApprovalRequests() <-chan provider.ApprovalRequest { return d.approve }

// ResolveApproval fails: there is never a request to resolve.
func (d *Driver) ResolveApproval(id string, _ string) error {
	return cascade.Newf(cascade.KindNotFound, "claude: no such approval request %q", id)
}

// Message is a capability probe (R-14.327 item 3): the one-shot spawn has
// no input channel (stdin is null, R-14.291), so a known job gets
// provider.ErrMessageUnsupported and nothing is written to the child; an
// unknown job gets ErrJobNotFound. It never returns nil.
func (d *Driver) Message(_ context.Context, id provider.AgentJobID, _ string) error {
	if _, err := d.lookup(id); err != nil {
		return err
	}
	return provider.ErrMessageUnsupported
}

// Status returns the job's current state.
func (d *Driver) Status(_ context.Context, id provider.AgentJobID) (provider.AgentRunState, error) {
	rec, err := d.lookup(id)
	if err != nil {
		return "", err
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return rec.state, nil
}

// Collect blocks until the job is finalized (or ctx ends) and returns its
// cached outcome.
func (d *Driver) Collect(ctx context.Context, id provider.AgentJobID) (provider.CollectResult, error) {
	rec, err := d.lookup(id)
	if err != nil {
		return provider.CollectResult{}, err
	}
	select {
	case <-rec.done:
	case <-ctx.Done():
		return provider.CollectResult{}, cascade.Wrapf(cascade.KindTimeout, ctx.Err(), "claude: Collect(%s) deadline", id)
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return rec.outcome, rec.outcomeErr
}

// finalize computes the terminal state once, after the group is empty.
func (d *Driver) finalize(rec *jobRecord) {
	res, err := d.outcome(rec)
	rec.mu.Lock()
	defer rec.mu.Unlock()
	switch {
	case rec.cancelRequested && !rec.custodyVerdict(err):
		rec.state, res, err = provider.AgentRunCancelled, provider.CollectResult{JobID: rec.id},
			cascade.New(cascade.KindCanceled, "claude: job was cancelled")
	case err != nil:
		rec.state, res = provider.AgentRunFailed, provider.CollectResult{JobID: rec.id}
	default:
		rec.state = provider.AgentRunAccepted // R-14.316: driver-level terminal success
	}
	rec.outcome, rec.outcomeErr = res, err
}

// custodyVerdict reports whether err is a verdict a Cancel must not relabel:
// the egress, overflow, canary, callback-channel and failed-scan verdicts. A straggler
// verdict is left out on purpose: Cancel kills the whole group, so a member
// still alive when the leader dies is the ladder's own work, not a leak.
// Sentinels are compared by identity (errors.Is compares Kind).
func (rec *jobRecord) custodyVerdict(err error) bool {
	if err == nil {
		return false
	}
	if rec.scanFailed {
		return true // set before finalize runs, by exitVerdict
	}
	return err == provider.ErrEgressSubstitutionFailed || err == ErrFilteredBufferOverflow ||
		err == ErrCredentialCanary || err == rec.channel.failure()
}

// outcome applies every verdict in order; the first failure wins.
func (d *Driver) outcome(rec *jobRecord) (provider.CollectResult, error) {
	rec.mu.Lock()
	egress, leak, exit := rec.egressFailed, rec.leak, rec.exitCode
	rec.mu.Unlock()
	channelErr := rec.channel.failure()
	switch {
	case egress:
		return provider.CollectResult{}, provider.ErrEgressSubstitutionFailed
	case rec.stdout.Overflowed() || rec.stderr.Overflowed():
		return provider.CollectResult{}, ErrFilteredBufferOverflow
	case rec.scanOut.Hit() || rec.scanErr.Hit() || rec.channel.scan.Hit():
		return provider.CollectResult{}, ErrCredentialCanary
	case channelErr != nil: // the root cause of the ladder it started
		return provider.CollectResult{}, channelErr
	case leak:
		return provider.CollectResult{}, ErrVendorAuthEnvLeak
	case exit != 0:
		return provider.CollectResult{}, d.exitVerdict(rec, exit)
	}
	ctx, cancel := context.WithTimeout(context.Background(), finalizeGitTimeout)
	defer cancel()
	artifacts, err := d.artifactsFor(ctx, rec)
	if err != nil {
		rec.markScanFailed()
		return provider.CollectResult{}, err
	}
	return d.assemble(rec, artifacts)
}

// markScanFailed records that the tree scan itself failed closed, so a Cancel
// that raced the exit does not relabel the run as cancelled.
func (rec *jobRecord) markScanFailed() {
	rec.mu.Lock()
	rec.scanFailed = true
	rec.mu.Unlock()
}

// exitVerdict is the failure for a non-zero exit. The tree is scanned first:
// a value the job wrote before it failed, or before a Cancel, must surface
// as the canary verdict and not as a plain exit status, and a scan that
// itself fails closed (an unreadable file, a broken index) surfaces as that
// error, as it does on the exit-0 path, so failing cannot downgrade it.
func (d *Driver) exitVerdict(rec *jobRecord, exit int) error {
	ctx, cancel := context.WithTimeout(context.Background(), finalizeGitTimeout)
	defer cancel()
	if _, err := d.artifactsFor(ctx, rec); err != nil {
		rec.markScanFailed() // a scan error is custody-grade here as on the exit-0 path
		if err == ErrCredentialCanary {
			return err
		}
		return cascade.Wrapf(cascade.KindIntegrity, err, "claude: harness exited with status %d and the tree scan failed", exit)
	}
	return cascade.Newf(cascade.KindUnavailable, "claude: harness exited with status %d", exit)
}

// assemble decodes stdout as the one result object and builds the closed
// rows: the result's done row (duration and result hash, no tool) followed
// by the rows the callback channel accepted, then the CollectResult.
func (d *Driver) assemble(rec *jobRecord, artifacts []string) (provider.CollectResult, error) {
	res, err := parseResult(bytes.TrimSpace(rec.stdout.Bytes()))
	if err != nil {
		return provider.CollectResult{}, err
	}
	resultRow := provider.AgentEvent{Kind: provider.DriverEventDone, Seq: 1,
		DataClass: rec.dataClass, Protocol: rec.protocol}
	rows := []JournalRow{toJournalRow(TranscriptRecord{Duration: time.Duration(res.DurationMS) * time.Millisecond,
		ResultHash: hashHex(res.Result)}, resultRow, rec.dataClass)}
	rows = append(rows, rec.channel.accepted()...)
	class := rec.dataClass
	for _, r := range rows {
		class = provider.JoinSensitivity(class, r.DataClass)
	}
	out, err := json.Marshal(rows)
	if err != nil {
		return provider.CollectResult{}, cascade.Wrapf(cascade.KindInternal, err, "claude: encoding journal rows")
	}
	if containsCanary(out, rec.canary.forms) {
		return provider.CollectResult{}, ErrCredentialCanary
	}
	cr := provider.CollectResult{JobID: rec.id, Output: string(out), Artifacts: artifacts, DataClass: class,
		SessionID: res.SessionID, Cost: &provider.CostEstimate{USD: *res.CostUSD, Source: provider.CostSourceCLIObservation}}
	if s := bytes.TrimSpace(res.Structured); len(s) > 0 && string(s) != "null" {
		cr.StructuredOutput = append([]byte(nil), s...)
	}
	return cr, nil
}

// resultObject is the revision-1 --output-format json result projection,
// decoded by this driver-local decoder; unknown fields are ignored.
type resultObject struct {
	Type       string          `json:"type"`
	Subtype    string          `json:"subtype"`
	IsError    *bool           `json:"is_error"`
	SessionID  string          `json:"session_id"`
	CostUSD    *float64        `json:"total_cost_usd"`
	NumTurns   *int            `json:"num_turns"`
	DurationMS int64           `json:"duration_ms"`
	Usage      json.RawMessage `json:"usage"`
	Result     string          `json:"result"`
	Structured json.RawMessage `json:"structured_output"`
}

// parseResult requires exactly one JSON object with every revision-1 key
// (testdata/protocol/1/result.json); anything else is
// ErrHarnessIncompatible, and is_error true fails the run.
func parseResult(line []byte) (resultObject, error) {
	var res resultObject
	dec := json.NewDecoder(bytes.NewReader(line))
	if err := dec.Decode(&res); err != nil || !decodedAll(dec) {
		return res, incompatible("harness output is not one result object")
	}
	if res.Type != "result" || res.Subtype == "" || res.IsError == nil || res.SessionID == "" ||
		res.CostUSD == nil || res.NumTurns == nil || len(res.Usage) == 0 {
		return res, incompatible("harness result object does not match protocol revision 1")
	}
	if *res.IsError {
		return res, cascade.Newf(cascade.KindUnavailable, "claude: harness reported an error result (%s)", res.Subtype)
	}
	return res, nil
}

func hashHex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
