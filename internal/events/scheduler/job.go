// Purpose: CronJob — the persisted schedule entry every Scheduler job is
//   built from — plus its Store persistence layout (task 1).
// Inputs: a provider.Store namespace (caller-supplied; per R-14.148's
//   precedent this is the SAME "queue" domain namespace the event bus
//   itself persists through — internal/storage/queue's DomainQueue), a
//   CronJob value.
// Outputs: the encoded/decoded CronJob record, or a cascade.KindIntegrity
//   error for a corrupt record, never a panic.
// Constraints: no bare time.Now (R-14.11) — every timestamp this file
//   reads or writes is either caller-supplied (job.LastFire) or absent
//   entirely (a job never fired has an empty, not zero-encoded, LastFire
//   marker, so "never fired" and "fired at the Unix epoch" are never
//   confused).
// SPORT: internal.events.scheduler.CronJob/ADDED (P1-E03-W1-S04-T4).

package scheduler

import (
	"context"
	"encoding/json"
	"sort"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// jobKeyPrefix namespaces this package's persisted CronJob records apart
// from the lock record (lockKey, lock.go) and from anything else sharing
// the same Store namespace (e.g. the event bus's own "event:"/"cursor:"
// keys, per R-14.148's shared-namespace precedent).
const jobKeyPrefix = "sched:job:"

func jobKey(id string) string { return jobKeyPrefix + id }

// CronJob is one persisted scheduler entry: which schedule (Spec) fires
// which registered runnable (Owner), and when it last fired. CronJob
// records survive daemon restart — they are read back from Store on every
// Activate, never reconstructed from in-memory state alone.
type CronJob struct {
	// ID uniquely identifies this job within the scheduler's namespace.
	ID string
	// Spec is the schedule string — either "@every <duration>" (Go
	// duration syntax) or a standard 5-field numeric cron expression
	// ("minute hour day-of-month month day-of-week"). See cron.go's
	// ParseSpec for the exact grammar.
	Spec string
	// Owner names the registered runnable this job fires
	// (Scheduler.RegisterRunnable's owner argument). A persisted job
	// whose Owner has no registered runnable is "orphaned" — see
	// scheduler.go's Activate.
	Owner string
	// LastFire is the instant this job last fired successfully, or the
	// zero time.Time if it has never fired. Skip-missed scheduling
	// (scheduler.go's Activate/Tick) deliberately never uses LastFire to
	// compute the next occurrence — it exists purely as an observability/
	// audit field.
	LastFire time.Time
}

// Interval reports the fixed interval an "@every <duration>" job fires at.
// ok is false for a standard 5-field cron spec (which has no single fixed
// interval) or an unparseable Spec. This is what
// TestRegisterRetentionJobs_WeeklyInterval (retention_register_test.go)
// uses to assert DomainPruner/VacuumJob are registered at exactly the
// 168h weekly default without reaching into this package's unexported
// schedule type.
func (j CronJob) Interval() (time.Duration, bool) {
	sched, err := ParseSpec(j.Spec)
	if err != nil {
		return 0, false
	}
	ev, ok := sched.(everySchedule)
	if !ok {
		return 0, false
	}
	return ev.interval, true
}

// jobRecord is CronJob's JSON wire shape. LastFire is omitted entirely
// (rather than encoded as the Go zero time's RFC3339 string) when the job
// has never fired, so "never fired" round-trips as a genuinely absent
// field rather than a magic sentinel string a future reader might
// misinterpret as a real timestamp.
type jobRecord struct {
	ID       string `json:"id"`
	Spec     string `json:"spec"`
	Owner    string `json:"owner"`
	LastFire string `json:"last_fire,omitempty"`
}

// encodeJob serializes j to its Store-persisted JSON form.
func encodeJob(j CronJob) ([]byte, error) {
	rec := jobRecord{ID: j.ID, Spec: j.Spec, Owner: j.Owner}
	if !j.LastFire.IsZero() {
		rec.LastFire = j.LastFire.UTC().Format(time.RFC3339Nano)
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindInternal, err, "scheduler: encode CronJob record")
	}
	return data, nil
}

// decodeJob deserializes data produced by encodeJob. It returns a
// cascade.KindIntegrity error — never a panic — for malformed JSON or an
// unparseable LastFire timestamp.
func decodeJob(data []byte) (CronJob, error) {
	var rec jobRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return CronJob{}, cascade.Wrap(cascade.KindIntegrity, err, "scheduler: corrupt CronJob record")
	}
	job := CronJob{ID: rec.ID, Spec: rec.Spec, Owner: rec.Owner}
	if rec.LastFire != "" {
		t, err := time.Parse(time.RFC3339Nano, rec.LastFire)
		if err != nil {
			return CronJob{}, cascade.Wrap(cascade.KindIntegrity, err, "scheduler: corrupt CronJob last_fire")
		}
		job.LastFire = t
	}
	return job, nil
}

// putJob upserts j's record in namespace.
func putJob(ctx context.Context, store provider.Store, namespace string, j CronJob) error {
	data, err := encodeJob(j)
	if err != nil {
		return err
	}
	if err := store.Put(ctx, namespace, jobKey(j.ID), data); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "scheduler: persist CronJob")
	}
	return nil
}

// getJob reads one persisted CronJob by id. It returns a
// cascade.KindNotFound error (the same Kind Store.Get itself returns) when
// no such job is persisted.
func getJob(ctx context.Context, store provider.Store, namespace, id string) (CronJob, error) {
	data, err := store.Get(ctx, namespace, jobKey(id))
	if err != nil {
		if cascade.HasKind(err, cascade.KindNotFound) {
			return CronJob{}, err
		}
		return CronJob{}, cascade.Wrap(cascade.KindUnavailable, err, "scheduler: read CronJob")
	}
	return decodeJob(data)
}

// listJobs returns every persisted CronJob in namespace, sorted by ID for
// deterministic iteration order (Activate and Tick both depend on this —
// TestSchedulerTick_DeterministicFireOrder asserts it directly).
func listJobs(ctx context.Context, store provider.Store, namespace string) ([]CronJob, error) {
	it, err := store.Scan(ctx, namespace, jobKeyPrefix)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "scheduler: scan CronJob records")
	}
	defer func() { _ = it.Close() }()

	var jobs []CronJob
	for it.Next(ctx) {
		job, derr := decodeJob(it.Value())
		if derr != nil {
			return nil, derr
		}
		jobs = append(jobs, job)
	}
	if iterErr := it.Err(); iterErr != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, iterErr, "scheduler: scan CronJob records")
	}
	sort.Slice(jobs, func(i, k int) bool { return jobs[i].ID < jobs[k].ID })
	return jobs, nil
}
