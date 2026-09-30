package hooks

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"sync"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose: turning one fire's outcome into its single HookFire: scrub,
//
//	store in the fires ring, publish to the hooks audit namespace. This is
//	the one call site that does so, so "every fire is audited" holds by
//	construction.
//
// Inputs: the hook, its Fire, the outcome, and the fire's seamState (the
//
//	tagged params and the rehydrated values, when the pipeline got that
//	far).
//
// Outputs: one HookFire published on AuditNamespace, one ring entry, and
//
//	the scrubbed error dispatchHook returns.
//
// Constraints: SECURITY. Every non-empty rehydrated value is replaced by
//
//	its tag in the error text before the record exists, on every path;
//	secret-shaped configured values are redacted as a second pass. The
//	record carries the tagged params hash, never params. The seam's zero
//	func runs exactly once per fire (seamState.finish).

// seamState is what one fire's pipeline goroutine hands back to
// dispatchHook, which may have stopped waiting for it (timeout). Guarded by
// mu because the abandoned goroutine can still write after finish.
type seamState struct {
	mu       sync.Mutex
	tagged   map[string]string
	pairs    []scrubPair
	zero     func()
	finished bool
}

// setTagged records the post-egress params.
func (s *seamState) setTagged(tagged map[string]string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tagged = tagged
}

// setPlain records the seam's output for scrubbing and takes ownership of
// zero. If dispatchHook already finished (timeout), zero runs now.
func (s *seamState) setPlain(plain map[string]string, zero func()) {
	s.mu.Lock()
	s.pairs = scrubPairs(s.tagged, plain)
	if !s.finished {
		s.zero = zero
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	if zero != nil {
		zero()
	}
}

// finish marks the fire done and runs the seam's zero, if one is held.
func (s *seamState) finish() {
	s.mu.Lock()
	s.finished = true
	zero := s.zero
	s.zero = nil
	s.mu.Unlock()
	if zero != nil {
		zero()
	}
}

// mayInvoke reports whether the runner may still be called: false once
// finish ran, so an abandoned pipeline never calls it after its fire ended.
func (s *seamState) mayInvoke() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.finished
}

// snapshot returns the tagged params and the scrub pairs.
func (s *seamState) snapshot() (map[string]string, []scrubPair) {
	if s == nil {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tagged, s.pairs
}

// record builds, scrubs, rings and publishes the fire's HookFire and
// returns it with the scrubbed error. Publish errors are swallowed: there
// is no caller left to hand them to, and the ring still holds the record.
func (d *Dispatcher) record(hook HookConfig, fire Fire, outcome hookOutcome, st *seamState) (HookFire, error) {
	tagged, pairs := st.snapshot()
	rec := HookFire{
		HookID: hook.ID, Namespace: fire.Namespace, Trigger: hook.Trigger, ActionType: hook.ActionType,
		EventSeq: fire.EventSeq, Depth: fire.Chain.Depth, ResultCode: outcome.result, Ts: d.clock.Now(),
	}
	if tagged != nil {
		rec.ParamsHash = paramsHash(tagged)
	}
	err := scrubError(outcome.err, pairs, hook.ActionParams)
	if err != nil {
		rec.ErrMsg = err.Error()
	}
	d.fires.add(rec)
	payload, merr := json.Marshal(rec)
	if merr != nil {
		payload = []byte(`{"marshal_error":true}`)
	}
	_, _ = d.bus.Publish(context.Background(), d.auditNamespace, EventKindHookFire, hook.ID, payload)
	return rec, err
}

// scrubPair is one plaintext string and the text that replaces it.
type scrubPair struct{ plain, tag string }

// scrubPairs pairs every non-empty rehydrated value with its tag (the
// tagged value under the same key, or a redaction marker when the seam
// added a key), plus the differing middle of the two so a plaintext
// fragment quoted without its surrounding text is caught too. Longest
// first, so a fragment never splits a longer match.
func scrubPairs(tagged, plain map[string]string) []scrubPair {
	var out []scrubPair
	for k, p := range plain {
		if p == "" {
			continue
		}
		tag, ok := tagged[k]
		if !ok {
			tag = redactedMarker
		}
		if p == tag {
			continue
		}
		out = append(out, scrubPair{plain: p, tag: tag})
		if mp, mt := differingMiddle(p, tag); mp != "" && mp != p {
			out = append(out, scrubPair{plain: mp, tag: mt})
		}
	}
	sort.Slice(out, func(i, j int) bool { return len(out[i].plain) > len(out[j].plain) })
	return out
}

// differingMiddle strips the common prefix and suffix of a and b.
func differingMiddle(a, b string) (string, string) {
	i := 0
	for i < len(a) && i < len(b) && a[i] == b[i] {
		i++
	}
	j := 0
	for j < len(a)-i && j < len(b)-i && a[len(a)-1-j] == b[len(b)-1-j] {
		j++
	}
	return a[i : len(a)-j], b[i : len(b)-j]
}

// redactedMarker replaces a secret with no tag to stand in for it.
const redactedMarker = "[REDACTED]"

// scrubError returns err with every scrub pair replaced and every
// secret-shaped configured value redacted, keeping err's cascade Kind.
func scrubError(err error, pairs []scrubPair, config map[string]string) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	for _, p := range pairs {
		msg = strings.ReplaceAll(msg, p.plain, p.tag)
	}
	msg = redactSecrets(msg, config)
	if kind, ok := cascade.KindOf(err); ok {
		return cascade.New(kind, strings.TrimPrefix(msg, kind.String()+": "))
	}
	return cascade.New(cascade.KindInternal, msg)
}

// redactSecrets returns msg with every literal occurrence of a
// secret-shaped value from params replaced by "[REDACTED]". See this
// file's package comment for what this does and does not guarantee.
func redactSecrets(msg string, params map[string]string) string {
	for _, v := range params {
		if v == "" {
			continue
		}
		if bad, _ := runtime.LooksLikeSecret(v); bad {
			msg = strings.ReplaceAll(msg, v, redactedMarker)
		}
	}
	return msg
}
