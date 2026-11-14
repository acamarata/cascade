package executive

// Purpose: the adapter mount. It subscribes the Adapter to each source
// namespace on the core bus and publishes every mapped event as a
// validated exec.* event on ExecNamespace.
//
// Inputs: the core events.Bus, the process secret detector, a logger.
// Outputs: exec.* events on ExecNamespace; Published, Refused and Dropped
// counters.
//
// Constraints: a mapped kind is published only when AdapterRows routes it
// live from that namespace and it is not in DormantKinds; one durable
// cursor per source namespace, named
// AdapterCursorPrefix+namespace; no second bus, queue or store. An event
// whose payload fails ValidatePayload or GuardSerialized is never
// published: it is counted as refused and logged once with sanitized text.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
)

// AdapterCursorPrefix prefixes the adapter's cursor name and its Producer.
const AdapterCursorPrefix = "executive-adapter/"

// AdapterBuffer is the Events buffer of each adapter subscription.
const AdapterBuffer = 64

// subjectFields names the payload field that identifies the subject of an
// event in each source namespace.
var subjectFields = map[string]string{nsJobs: "job_id", nsLease: "holder", nsCI: "run_id", nsNotify: "id"}

// runPrefixes prefixes a subject used as the RunID in these namespaces.
var runPrefixes = map[string]string{nsCI: "ci:", nsNotify: "notify:"}

// Translate turns ev, read from ns and mapped onto kind, into an exec.*
// draft for ExecNamespace. A missing subject, a payload kind has no
// translation for, or a body that fails ValidatePayload or
// GuardSerialized is an error and yields no draft.
func Translate(ns string, ev events.Event, kind Kind, det *secrets.Detector) (events.Event, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(ev.Payload, &fields); err != nil {
		return events.Event{}, fmt.Errorf("%w: bus payload is not a JSON object", ErrInvalidExecPayload)
	}
	subject, err := subjectOf(ns, fields)
	if err != nil {
		return events.Event{}, err
	}
	body, err := execBody(kind, subject, fields)
	if err != nil {
		return events.Event{}, err
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return events.Event{}, fmt.Errorf("%w: marshal", ErrInvalidExecPayload)
	}
	if err := ValidatePayload(kind, PayloadV1, raw); err != nil {
		return events.Event{}, err
	}
	if err := GuardSerialized(det, raw); err != nil {
		return events.Event{}, err
	}
	run := ev.RunID
	if run == "" {
		run = runPrefixes[ns] + subject
	}
	stream, err := DeriveStreamID(RunID(run), ProjectID(ev.ProjectID))
	if err != nil {
		return events.Event{}, err
	}
	return events.Event{
		Kind: events.EventKind(kind), Payload: raw, PayloadVersion: uint32(PayloadV1),
		RunID: run, ProjectID: ev.ProjectID, StreamID: string(stream),
		Producer: AdapterCursorPrefix + ns, Source: ns + ":" + strconv.FormatUint(ev.Seq, 10),
	}, nil
}

// subjectOf reads the subject of an event from ns: a non-empty string, or
// a decimal run_id for ci_results.
func subjectOf(ns string, fields map[string]json.RawMessage) (string, error) {
	name, ok := subjectFields[ns]
	if !ok {
		return "", fmt.Errorf("%w: namespace has no subject field", ErrInvalidExecPayload)
	}
	var subject string
	if ns == nsCI {
		var n uint64
		if err := json.Unmarshal(fields[name], &n); err == nil && n > 0 {
			subject = strconv.FormatUint(n, 10)
		}
	} else if err := json.Unmarshal(fields[name], &subject); err != nil {
		subject = ""
	}
	if subject == "" {
		return "", fmt.Errorf("%w: missing %s", ErrInvalidExecPayload, name)
	}
	return subject, nil
}

// execBody builds the v1 body of kind for subject.
func execBody(kind Kind, subject string, fields map[string]json.RawMessage) (any, error) {
	switch kind { //nolint:exhaustive // only the kinds an adapter row maps have a translation; every other kind is refused through the default
	case KindTaskStarted, KindTaskCompleted:
		return TaskPayload{TaskID: subject}, nil
	case KindTaskFailed:
		var attempts int64
		if err := json.Unmarshal(fields["consecutive_failed_attempts"], &attempts); err != nil {
			attempts = 0
		}
		return TaskFailedPayload{TaskID: subject, ConsecutiveFailedAttempts: attempts}, nil
	case KindVerifyRequested, KindVerifyPassed, KindVerifyFailed:
		return VerifyPayload{SubjectID: subject}, nil
	case KindLeaseAcquired, KindLeaseConflict, KindLeaseExpired:
		return WorkspacePayload{TaskID: subject}, nil
	case KindDecisionRequired:
		return DecisionPayload{SubjectID: subject}, nil
	default:
		return nil, fmt.Errorf("%w: kind has no adapter translation", ErrInvalidExecPayload)
	}
}

// liveRoutes builds the namespace-to-kind routing from AdapterRows: every
// row that maps to an exec kind, less DormantKinds. A mapped kind outside
// it has no live source and is never published.
func liveRoutes() map[string]map[Kind]bool {
	dormant := make(map[Kind]bool)
	for _, k := range DormantKinds() {
		dormant[k] = true
	}
	routes := make(map[string]map[Kind]bool)
	for _, r := range AdapterRows() {
		if r.Exec == "" || dormant[r.Exec] {
			continue
		}
		if routes[r.Namespace] == nil {
			routes[r.Namespace] = make(map[Kind]bool)
		}
		routes[r.Namespace][r.Exec] = true
	}
	return routes
}

// errDormantKind refuses a mapped kind the routing does not hold live.
var errDormantKind = errors.New("executive: mapped kind has no live adapter route")

// Mounted is a running adapter mount.
type Mounted struct {
	bus       *events.Bus
	det       *secrets.Detector
	adapter   *Adapter
	routes    map[string]map[Kind]bool
	logger    *slog.Logger
	subs      []*events.Subscription
	wg        sync.WaitGroup
	stopOnce  sync.Once
	stopErr   error
	published atomic.Uint64
	refused   atomic.Uint64
}

// Mount subscribes the adapter to every SourceNamespaces namespace and
// starts publishing. A nil bus or detector is cascade.KindInvalidInput; a
// failed Subscribe (KindConflict when another mount holds the cursor)
// unsubscribes the earlier namespaces and is returned.
func Mount(ctx context.Context, bus *events.Bus, det *secrets.Detector, logger *slog.Logger) (*Mounted, error) {
	if bus == nil || det == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "executive: adapter mount needs a bus and a detector")
	}
	if logger == nil {
		logger = slog.Default()
	}
	m := &Mounted{bus: bus, det: det, adapter: NewAdapter(logger), routes: liveRoutes(), logger: logger}
	namespaces := SourceNamespaces()
	for _, ns := range namespaces {
		sub, err := bus.Subscribe(ctx, ns, AdapterCursorPrefix+ns, AdapterBuffer)
		if err != nil {
			for _, s := range m.subs {
				_ = s.Unsubscribe()
			}
			return nil, err
		}
		m.subs = append(m.subs, sub)
	}
	run := context.WithoutCancel(ctx)
	for i, sub := range m.subs {
		m.wg.Add(1)
		go m.loop(run, namespaces[i], sub)
	}
	return m, nil
}

// loop handles every event of one subscription until it stops.
func (m *Mounted) loop(ctx context.Context, ns string, sub *events.Subscription) {
	defer m.wg.Done()
	for ev := range sub.Events {
		m.handle(ctx, ns, ev)
	}
	select {
	case err := <-sub.Errs:
		m.logger.Warn("executive adapter subscription failed",
			slog.String("namespace", ns), slog.String("error", SanitizeError(m.det, err)))
	default:
	}
}

// handle maps one event and publishes its translation.
func (m *Mounted) handle(ctx context.Context, ns string, ev events.Event) {
	kind, outcome := m.adapter.Map(ns, ev)
	if outcome != OutcomeMapped {
		return
	}
	if err := m.publish(ctx, ns, ev, kind); err != nil {
		m.refused.Add(1)
		m.logger.Warn("executive adapter refused event",
			slog.String("namespace", ns), slog.String("kind", string(kind)), slog.String("error", SanitizeError(m.det, err)))
		return
	}
	m.published.Add(1)
}

// publish translates ev and publishes it, unless kind is not routed live
// from ns.
func (m *Mounted) publish(ctx context.Context, ns string, ev events.Event, kind Kind) error {
	if !m.routes[ns][kind] {
		return errDormantKind
	}
	draft, err := Translate(ns, ev, kind, m.det)
	if err != nil {
		return err
	}
	_, err = m.bus.PublishEvent(ctx, ExecNamespace, draft)
	return err
}

// Stop unsubscribes every namespace and waits for the handlers to finish.
// It is idempotent; later calls return the first call's result.
func (m *Mounted) Stop() error {
	m.stopOnce.Do(func() {
		var errs []error
		for _, s := range m.subs {
			if err := s.Unsubscribe(); err != nil && !cascade.HasKind(err, cascade.KindNotFound) {
				errs = append(errs, err)
			}
		}
		m.wg.Wait()
		m.stopErr = errors.Join(errs...)
	})
	return m.stopErr
}

// Published returns the number of exec.* events the mount has published.
func (m *Mounted) Published() uint64 { return m.published.Load() }

// Refused returns the number of mapped events the mount refused to publish.
func (m *Mounted) Refused() uint64 { return m.refused.Load() }

// Dropped returns the number of events the adapter dropped as unmapped.
func (m *Mounted) Dropped() uint64 { return m.adapter.Dropped() }
