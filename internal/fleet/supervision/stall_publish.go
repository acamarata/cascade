package supervision

// Purpose (this file): the "supervision.stalled" bus event, one per stall
// episode. The Detector decides when to publish (stall.go tracks the
// episode); this file only shapes and sends the notification.
//
// Inputs: a StallEvent and an EventPublisher (*events.Bus satisfies it).
// Outputs: one events.NotificationPayload on namespace "supervision",
// kind "supervision.stalled", source "supervision.stall".
// Constraints: the payload carries ids, the stall kind and times only,
// never session or user content.
//
// SPORT: fleet.supervision.stall-rungs/ADDED (P1-SUP-03).

import (
	"context"
	"encoding/json"
	"strconv"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/pkg/cascade"
)

const (
	stalledNamespace                  = "supervision"
	stalledKind      events.EventKind = "supervision.stalled"
	stalledSource                     = "supervision.stall"
)

// EventPublisher is the publish side of the event bus. *events.Bus
// satisfies it.
type EventPublisher interface {
	Publish(ctx context.Context, namespace string, kind events.EventKind, source string, payload []byte) (events.Event, error)
}

// StallPublisher announces a stall episode.
type StallPublisher interface {
	PublishStalled(ctx context.Context, ev StallEvent) error
}

type busStallPublisher struct{ bus EventPublisher }

// NewBusStallPublisher returns a StallPublisher that publishes
// supervision.stalled on bus.
func NewBusStallPublisher(bus EventPublisher) StallPublisher {
	return &busStallPublisher{bus: bus}
}

// stalledBody is the notification body: the three fields the contract names.
type stalledBody struct {
	StallKind      string `json:"stall_kind"`
	StalledSince   int64  `json:"stalled_since"`
	ElapsedSeconds int64  `json:"elapsed_seconds"`
}

// stallNotificationID is "stall:<session>:<stalled_since ms|unknown>".
func stallNotificationID(ev StallEvent) string {
	since := "unknown"
	if ev.StalledSince > 0 {
		since = strconv.FormatInt(ev.StalledSince, 10)
	}
	return "stall:" + ev.SessionID + ":" + since
}

// PublishStalled implements StallPublisher.
func (p *busStallPublisher) PublishStalled(ctx context.Context, ev StallEvent) error {
	if p == nil || p.bus == nil {
		return cascade.New(cascade.KindUnavailable, "supervision: stall publisher has no bus")
	}
	body, err := json.Marshal(stalledBody{StallKind: string(ev.StallKind), StalledSince: ev.StalledSince, ElapsedSeconds: ev.ElapsedSeconds})
	if err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "supervision: encoding stall body")
	}
	payload := events.EncodeNotificationPayload(events.NotificationPayload{
		ID:            stallNotificationID(ev),
		CorrelationID: ev.SessionID,
		TargetSession: ev.SessionID,
		DeepLink:      "cascade://fleet/sessions/" + ev.SessionID,
		Visibility:    "private",
		Body:          body,
	})
	_, err = p.bus.Publish(ctx, stalledNamespace, stalledKind, stalledSource, payload)
	return err
}

// publishOnce publishes ev's episode once. An episode runs from the first
// stall record for a session until Touch or an unwatch ends it. A failed
// publish leaves the episode unpublished, so the next Poll or Observe
// retries it; a detector without a publisher does nothing. An ev whose
// epoch is no longer current belongs to an episode that already ended and
// is dropped: endEpisode takes pubMu, so the check cannot go stale before
// the publish below.
func (d *Detector) publishOnce(ctx context.Context, ev StallEvent, epoch uint64) error {
	if d.pub == nil {
		return nil
	}
	d.pubMu.Lock()
	defer d.pubMu.Unlock()
	d.mu.Lock()
	done := d.published[ev.SessionID] || d.epoch[ev.SessionID] != epoch
	d.mu.Unlock()
	if done {
		return nil
	}
	if err := d.pub.PublishStalled(ctx, ev); err != nil {
		return err
	}
	d.mu.Lock()
	d.published[ev.SessionID] = true
	d.mu.Unlock()
	return nil
}

// endEpisode closes sessionID's stall episode: the next stall record for it
// publishes again and its RungDelay clock starts fresh.
func (d *Detector) endEpisode(sessionID string) {
	d.pubMu.Lock()
	defer d.pubMu.Unlock()
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.published, sessionID)
	delete(d.advanced, sessionID)
	d.epoch[sessionID]++
}

// episodeEpoch is sessionID's count of ended episodes. Read it before
// observing the state a stall record is built from.
func (d *Detector) episodeEpoch(sessionID string) uint64 {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.epoch[sessionID]
}
