package executive

// Purpose: the adapter table from the real core-bus kinds onto the closed
// exec.* kinds, the producer rows for exec.* kinds no bus kind feeds, and
// the Adapter that applies the table to one bus event.
//
// Inputs: a namespace and an events.Event read from the core bus.
// Outputs: the mapped exec.* Kind and a MapOutcome. A skip row reports
// OutcomeSkipped; an unlisted kind, bad JSON or an unmatched field value
// reports OutcomeDropped, counts once and logs one WARN.
//
// Constraints: data only. This file imports nothing but the standard
// library and internal/events and never publishes or subscribes; the
// mount that does is a separate component. It never guesses a kind for
// an unlisted (namespace, kind, value).

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"sync/atomic"

	"github.com/acamarata/cascade/internal/events"
)

// MapOutcome is the closed result of mapping one bus event.
type MapOutcome string

// The closed MapOutcome values.
const (
	// OutcomeMapped means a row mapped the event onto an exec.* kind.
	OutcomeMapped MapOutcome = "mapped"
	// OutcomeSkipped means a skip row matched; the event has no exec.* kind.
	OutcomeSkipped MapOutcome = "skipped"
	// OutcomeDropped means no row matched; the event was counted and logged.
	OutcomeDropped MapOutcome = "dropped"
)

// AdapterRow maps one (namespace, bus kind[, field=value]) onto an exec.*
// kind. An empty Exec marks a skip row.
type AdapterRow struct {
	Namespace string           `json:"namespace"`
	BusKind   events.EventKind `json:"bus_kind"`
	Field     string           `json:"field,omitempty"`
	Value     json.RawMessage  `json:"value,omitempty"`
	Exec      Kind             `json:"exec,omitempty"`
	Live      bool             `json:"live"`
}

// ProducerRow names the producer of an exec.* kind no adapter row maps.
type ProducerRow struct {
	Exec     Kind   `json:"exec"`
	Producer string `json:"producer"`
	Live     bool   `json:"live"`
}

// The bus namespaces and kind literals the table reads. Their owners are
// internal/jobs, internal/rpc and internal/ci; this package may not import
// them, so the adapter test pins each literal to its owner's constant.
const (
	nsJobs   = "jobs"
	nsLease  = "jobs.lease"
	nsCI     = "ci_results"
	nsNotify = "notify"
	nsPolicy = "policy"

	busJobLeased       events.EventKind = "job.leased"
	busJobTransitioned events.EventKind = "job.transitioned"
	busJobCompleted    events.EventKind = "job.completed"
	busJobFailed       events.EventKind = "job.failed"
	busLeaseAcquired   events.EventKind = "jobs.lease.acquired"
	busLeaseContended  events.EventKind = "jobs.lease.contended"
	busLeaseRenewed    events.EventKind = "jobs.lease.renewed"
	busLeaseReleased   events.EventKind = "jobs.lease.released"
	busLeaseExpired    events.EventKind = "jobs.lease.expired"
	busCIRunCompleted  events.EventKind = "ci.run.completed"
	busNotification    events.EventKind = "notify.notification"
)

// wakeEngineProducer names the producer of the three executive-group kinds.
const wakeEngineProducer = "wake engine"

// noProducer marks an exec.* kind no shipped component produces yet.
const noProducer = "none"

func row(ns string, kind events.EventKind, exec Kind) AdapterRow {
	return AdapterRow{Namespace: ns, BusKind: kind, Exec: exec, Live: true}
}

func valueRow(ns string, kind events.EventKind, field, value string, exec Kind) AdapterRow {
	return AdapterRow{Namespace: ns, BusKind: kind, Field: field, Value: json.RawMessage(value), Exec: exec, Live: true}
}

func toRow(state string, exec Kind) AdapterRow {
	return valueRow(nsJobs, busJobTransitioned, "to", `"`+state+`"`, exec)
}

// adapterRows builds the table in its declared order.
func adapterRows() []AdapterRow {
	return []AdapterRow{
		row(nsJobs, busJobLeased, ""),
		toRow("leased", ""),
		toRow("running", KindTaskStarted),
		toRow("verifying", KindVerifyRequested),
		toRow("reviewing", ""),
		toRow("accepted", ""),
		toRow("rejected", ""),
		toRow("cancelling", ""),
		toRow("cancelled", ""),
		toRow("failed", ""),
		row(nsJobs, busJobCompleted, KindTaskCompleted),
		row(nsJobs, busJobFailed, KindTaskFailed),
		row(nsLease, busLeaseAcquired, KindLeaseAcquired),
		row(nsLease, busLeaseContended, KindLeaseConflict),
		row(nsLease, busLeaseRenewed, ""),
		row(nsLease, busLeaseReleased, ""),
		row(nsLease, busLeaseExpired, KindLeaseExpired),
		valueRow(nsCI, busCIRunCompleted, "passed", "true", KindVerifyPassed),
		valueRow(nsCI, busCIRunCompleted, "passed", "false", KindVerifyFailed),
		{Namespace: nsNotify, BusKind: busNotification, Exec: KindDecisionRequired, Live: false},
	}
}

// AdapterRows returns a fresh copy of the adapter table in declared order.
func AdapterRows() []AdapterRow {
	return adapterRows()
}

// ProducerRows returns, in AllKinds order, one row for every exec.* kind no
// adapter row maps.
func ProducerRows() []ProducerRow {
	mapped := make(map[Kind]bool)
	for _, r := range adapterRows() {
		if r.Exec != "" {
			mapped[r.Exec] = true
		}
	}
	var out []ProducerRow
	for _, k := range AllKinds {
		if mapped[k] {
			continue
		}
		switch k { //nolint:exhaustive // only the three wake kinds have a live producer; every other exec kind is dormant through the default
		case KindT0WakeRequested, KindT0Awake, KindT0Sleeping:
			out = append(out, ProducerRow{Exec: k, Producer: wakeEngineProducer, Live: true})
		default:
			out = append(out, ProducerRow{Exec: k, Producer: noProducer, Live: false})
		}
	}
	return out
}

// SourceNamespaces returns the bus namespaces the adapter reads, in order.
// policy carries no rows: every policy event is dropped.
func SourceNamespaces() []string {
	return []string{nsJobs, nsLease, nsCI, nsNotify, nsPolicy}
}

// DormantKinds returns, in AllKinds order, every exec.* kind with no live
// source: the producer rows that are not Live plus the kinds whose only
// adapter rows are not Live.
func DormantKinds() []Kind {
	dormant := make(map[Kind]bool)
	for _, p := range ProducerRows() {
		if !p.Live {
			dormant[p.Exec] = true
		}
	}
	live := make(map[Kind]bool)
	for _, r := range adapterRows() {
		if r.Exec != "" && r.Live {
			live[r.Exec] = true
		}
	}
	for _, r := range adapterRows() {
		if r.Exec != "" && !live[r.Exec] {
			dormant[r.Exec] = true
		}
	}
	var out []Kind
	for _, k := range AllKinds {
		if dormant[k] {
			out = append(out, k)
		}
	}
	return out
}

// Adapter applies the adapter table to bus events. It is safe for
// concurrent use.
type Adapter struct {
	rows    []AdapterRow
	logger  *slog.Logger
	dropped atomic.Uint64
}

// NewAdapter returns an Adapter that logs drops to logger (slog.Default()
// when nil).
func NewAdapter(logger *slog.Logger) *Adapter {
	if logger == nil {
		logger = slog.Default()
	}
	return &Adapter{rows: adapterRows(), logger: logger}
}

// Map returns the exec.* kind for ev read from namespace. A skip row gives
// ("", OutcomeSkipped). No matching row, bad JSON or no value match gives
// ("", OutcomeDropped), raises Dropped by one and logs one WARN naming the
// namespace and kind, never the payload.
func (a *Adapter) Map(namespace string, ev events.Event) (Kind, MapOutcome) {
	var fields map[string]json.RawMessage
	decoded := false
	for _, r := range a.rows {
		if r.Namespace != namespace || r.BusKind != ev.Kind {
			continue
		}
		if r.Field != "" {
			if !decoded {
				if err := json.Unmarshal(ev.Payload, &fields); err != nil {
					return a.drop(namespace, ev.Kind, "payload is not a JSON object")
				}
				decoded = true
			}
			if !fieldEquals(fields[r.Field], r.Value) {
				continue
			}
		}
		if r.Exec == "" {
			return "", OutcomeSkipped
		}
		return r.Exec, OutcomeMapped
	}
	return a.drop(namespace, ev.Kind, "no adapter row")
}

// fieldEquals reports whether raw, compacted, equals want byte for byte.
func fieldEquals(raw, want json.RawMessage) bool {
	if raw == nil {
		return false
	}
	// raw came out of a successful Unmarshal, so it is valid JSON and
	// Compact cannot fail.
	var buf bytes.Buffer
	_ = json.Compact(&buf, raw)
	return bytes.Equal(buf.Bytes(), want)
}

func (a *Adapter) drop(namespace string, kind events.EventKind, reason string) (Kind, MapOutcome) {
	a.dropped.Add(1)
	a.logger.Warn("executive adapter dropped event",
		slog.String("namespace", namespace), slog.String("kind", string(kind)), slog.String("reason", reason))
	return "", OutcomeDropped
}

// Dropped returns the number of events Map has dropped.
func (a *Adapter) Dropped() uint64 {
	return a.dropped.Load()
}
