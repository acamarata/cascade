// Package agents holds the daemon-side composition around external agent
// drivers: the per-spawn egress proxy bound to the audit log (proxy.go) and
// the lease-marker process sweep (reap.go).
package agents

// Purpose: the per-spawn containment the driver shim composes. StartSpawnProxy
// starts one egress proxy for one driver spawn; ProxyJournal binds that
// proxy's journal to the redacting audit log, so every connection decision
// is one audit event and every dial is a recorded effect (intent before the
// dial, confirm or fail after it).
//
// Constraints: the proxy only ever dials through egressproxy.ProductionDial
// (the resolved-address guard) over an operator allow list; a nil list denies
// everything. Tunneled bytes are never read or journaled. An effect-log error
// is returned to the proxy, which then refuses and never dials; a decision row
// the sink cannot append is returned too, and a refusal stays a refusal.
// The spawn owner closes the proxy at release; after Close the old port
// refuses connections.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"sync"
	"time"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/providers/agents/egressproxy"
	"github.com/acamarata/cascade/pkg/cascade"
)

// Audit vocabulary bound by the proxy journal (egressproxy/decision.go).
const (
	actorPrefix  = "agent-driver:"
	actionDial   = "agent.egress.connect"
	effectPrefix = "agent.egress:"
	verdictAllow = "allow"
	verdictDeny  = "deny"
	maxKeyTries  = 4096 // ErrEffectExists retries before the journal gives up
)

// settleTimeout bounds a journal write that outlives its proxy (a test shortens it).
var settleTimeout = 10 * time.Second

// AuditSink appends one event to the audit log. *audit.Log satisfies it.
type AuditSink interface {
	Append(ctx context.Context, e audit.Event) (audit.Record, error)
}

// StartSpawnProxy starts the egress proxy for one driver spawn. allow is that
// driver's [agents.egress.<driver-id>] list (nil denies every destination); j
// is the spawn's ProxyJournal. The caller puts the proxy's Env() in the child
// environment and closes the proxy when the spawn is released.
func StartSpawnProxy(ctx context.Context, driver egressproxy.DriverID, jobID string,
	allow egressproxy.DestinationAllowlist, j egressproxy.Journal) (*egressproxy.Proxy, error) {
	return startSpawnProxy(ctx, driver, jobID, allow, j, egressproxy.ProductionDial())
}

// startSpawnProxy is StartSpawnProxy over an explicit dialer; only tests pass
// anything but ProductionDial (its address guard refuses loopback).
func startSpawnProxy(ctx context.Context, driver egressproxy.DriverID, jobID string,
	allow egressproxy.DestinationAllowlist, j egressproxy.Journal, dial egressproxy.DialFunc) (*egressproxy.Proxy, error) {
	return egressproxy.Start(ctx, egressproxy.Options{
		DriverID: driver, JobID: jobID, Allow: allow, Dial: dial, Journal: j,
	})
}

// ProxyJournal binds a proxy's Journal to the audit log. A decide row (a
// refusal before any dial, or a closed tunnel) is one audit.Event of kind
// policy.decide. The intent row is BeginEffect, written before the dial; the
// connected row is ConfirmEffect, a failed or forbidden dial is FailEffect.
// A nil sink or effect writer yields a journal that errors on every row, so
// the proxy refuses everything rather than running unaudited.
func ProxyJournal(sink AuditSink, effects audit.EffectWriter) egressproxy.Journal {
	j := &proxyJournal{
		sink: sink, effects: effects,
		slots: make(map[egressproxy.Destination]chan struct{}),
		held:  make(map[egressproxy.Destination]audit.EffectHandle),
	}
	return j.record
}

// proxyJournal is the per-spawn journal state. The egressproxy.Decision row
// carries no connection identity, so one destination has at most one dial
// between its intent row and its terminal row (slots); that is what lets a
// confirm row find its own intent handle (held).
type proxyJournal struct {
	sink    AuditSink
	effects audit.EffectWriter

	mu    sync.Mutex
	next  uint64
	slots map[egressproxy.Destination]chan struct{}
	held  map[egressproxy.Destination]audit.EffectHandle
}

// record routes one row by phase.
func (j *proxyJournal) record(ctx context.Context, d egressproxy.Decision) error {
	if j.sink == nil || j.effects == nil {
		return cascade.New(cascade.KindUnavailable, "agents: the egress journal has no audit sink")
	}
	switch d.Phase {
	case egressproxy.PhaseIntent:
		return j.intent(ctx, d)
	case egressproxy.PhaseConfirm:
		return j.confirm(ctx, d)
	case egressproxy.PhaseDecide: // a refusal or a closed tunnel is an audit event only
	}
	return j.appendEvent(ctx, d)
}

// intent claims the destination and records the dial's intent. A failure
// frees the claim at once: the proxy never dials after an intent error.
func (j *proxyJournal) intent(ctx context.Context, d egressproxy.Decision) error {
	if err := j.acquire(ctx, d.Destination); err != nil {
		return err
	}
	owned := false
	// Free the claim on every exit (a panic included) until the terminal row owns it.
	defer func() {
		if !owned {
			j.release(d.Destination)
		}
	}()
	h, err := j.begin(ctx, d)
	if err != nil {
		// A handle with the error is a committed intent whose notification
		// failed: the dial will not run, so close it as failed.
		if h.IntentSeq != 0 {
			wctx, cancel := settleContext(ctx)
			defer cancel()
			_, _ = j.effects.FailEffect(wctx, h, string(egressproxy.ReasonJournalUnavailable))
		}
		return err
	}
	j.mu.Lock()
	j.held[d.Destination] = h
	j.mu.Unlock()
	owned = true
	return nil
}

// begin commits the intent effect under the first free key.
func (j *proxyJournal) begin(ctx context.Context, d egressproxy.Decision) (audit.EffectHandle, error) {
	explain, err := explainJSON(d)
	if err != nil {
		return audit.EffectHandle{}, err
	}
	actor := actorPrefix + string(d.DriverID)
	for range maxKeyTries {
		key := j.nextKey(d.JobID)
		h, err := j.effects.BeginEffect(ctx, audit.EffectRequest{
			Kind: audit.KindExternalEffect, Actor: actor, Action: actionDial, Key: key,
			ParamsHash: audit.HashParams([]byte(string(d.Destination))), Explain: explain,
		})
		if err == nil || !carries(err, audit.ErrEffectExists) {
			return h, err
		}
	}
	return audit.EffectHandle{}, cascade.New(cascade.KindConflict, "agents: no free egress effect key for this job")
}

// confirm closes the open effect: connected confirms it, a failed or
// forbidden dial fails it. A closed row only audits the end of the tunnel.
// Either terminal write frees the destination's claim, a failed one too: the
// dial happened or did not, and an unconfirmed intent stays pending for
// recovery (unknown-outcome), never re-dialed.
func (j *proxyJournal) confirm(ctx context.Context, d egressproxy.Decision) error {
	if d.Reason == egressproxy.ReasonClosed {
		return j.appendEvent(ctx, d)
	}
	j.mu.Lock()
	h, ok := j.held[d.Destination]
	delete(j.held, d.Destination)
	j.mu.Unlock()
	if !ok {
		return cascade.New(cascade.KindInternal, "agents: a confirm row arrived with no open egress intent")
	}
	defer j.release(d.Destination)
	// Terminal writes survive the proxy closing (a Close racing a finished dial
	// settles its effect) but are bounded, so a stalled writer cannot hold Close.
	ctx, cancel := settleContext(ctx)
	defer cancel()
	var err error
	if d.Reason == egressproxy.ReasonConnected {
		_, err = j.effects.ConfirmEffect(ctx, h, string(d.Reason))
	} else {
		_, err = j.effects.FailEffect(ctx, h, string(d.Reason))
	}
	return err
}

// settleContext detaches a terminal write from ctx's cancellation, bounded.
func settleContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), settleTimeout)
}

// appendEvent writes the policy.decide event for d.
func (j *proxyJournal) appendEvent(ctx context.Context, d egressproxy.Decision) error {
	explain, err := explainJSON(d)
	if err != nil {
		return err
	}
	verdict := verdictDeny
	if d.Allowed {
		verdict = verdictAllow
	}
	ctx, cancel := settleContext(ctx)
	defer cancel()
	_, err = j.sink.Append(ctx, audit.Event{
		Kind: audit.KindPolicyDecide, Actor: actorPrefix + string(d.DriverID), Action: actionDial,
		Verdict: verdict, Outcome: string(d.Reason), Explain: explain,
	})
	return err
}

// acquire waits for the destination's single in-flight dial slot.
func (j *proxyJournal) acquire(ctx context.Context, dest egressproxy.Destination) error {
	j.mu.Lock()
	slot, ok := j.slots[dest]
	if !ok {
		slot = make(chan struct{}, 1)
		j.slots[dest] = slot
	}
	j.mu.Unlock()
	select {
	case slot <- struct{}{}:
		return nil
	case <-ctx.Done():
		return cascade.Wrap(cascade.KindCanceled, ctx.Err(), "agents: waiting for the egress dial slot")
	}
}

// release frees the destination's slot; releasing a free slot is a no-op.
func (j *proxyJournal) release(dest egressproxy.Destination) {
	j.mu.Lock()
	slot := j.slots[dest]
	j.mu.Unlock()
	select {
	case <-slot:
	default:
	}
}

// nextKey is "agent.egress:<job>:<n>", n counting up from 1 per journal. A
// job id outside the effect-key alphabet is replaced by a digest of it.
func (j *proxyJournal) nextKey(jobID string) string {
	j.mu.Lock()
	j.next++
	n := j.next
	j.mu.Unlock()
	return effectPrefix + keyJob(jobID) + ":" + strconv.FormatUint(n, 10)
}

// keyJob returns jobID when it is a short, key-safe token, else "h" plus a
// digest prefix of it.
func keyJob(jobID string) string {
	ok := jobID != "" && len(jobID) <= 64
	for i := 0; ok && i < len(jobID); i++ {
		c := jobID[i]
		ok = c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-'
	}
	if ok {
		return jobID
	}
	sum := sha256.Sum256([]byte(jobID))
	return "h" + hex.EncodeToString(sum[:])[:32]
}

// explainJSON is the {destination, job_id, phase} rationale of a row.
func explainJSON(d egressproxy.Decision) (json.RawMessage, error) {
	raw, err := json.Marshal(struct {
		Destination string `json:"destination"`
		JobID       string `json:"job_id"`
		Phase       string `json:"phase"`
	}{string(d.Destination), d.JobID, string(d.Phase)})
	if err != nil {
		return nil, cascade.Wrap(cascade.KindInternal, err, "agents: encoding an egress audit rationale")
	}
	return raw, nil
}

// carries reports whether target itself (pointer identity, never Kind
// equality) is anywhere in err's wrap chain.
func carries(err error, target *cascade.Error) bool {
	for err != nil {
		if e, ok := err.(*cascade.Error); ok && e == target {
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
