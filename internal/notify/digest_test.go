package notify

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/events"
)

// seedAccumulated fills router's accumulation buffer through the real gate
// (queueSet.enqueue, the call every producer makes) and then closes the gate,
// which is exactly the sequence AwayController performs on the return edge:
// accumulate while Away, accumulation off inside the critical section that
// closes the episode, digest compiled after that.
func seedAccumulated(router *NotificationRouter, items ...Notification) {
	router.SetAccumulate(true)
	for _, n := range items {
		router.queues.enqueue(n)
	}
	router.SetAccumulate(false)
}

// seedAccumulatedFromBus is seedAccumulated over the bus-decode path, so the
// buffered notification carries the Class and Priority the normative
// source-Kind mapping really assigns it.
func seedAccumulatedFromBus(router *NotificationRouter, kind events.EventKind, id, targetScope string) {
	router.SetAccumulate(true)
	routeBusNotification(router, kind, id, targetScope)
	router.SetAccumulate(false)
}

func newTestDigestCompiler(t *testing.T, now time.Time, cfg AwayConfig) (*DigestCompiler, *NotificationRouter, *Registry) {
	t.Helper()
	queues := newQueueSet(DefaultConfig(), silentLogger())
	registry := NewRegistry()
	clk := fixedClock{now: now}
	router := NewNotificationRouter(queues, clk, silentLogger())
	notifier := NewNotifier(queues, clk)
	dc := NewDigestCompiler(registry, notifier, router, clk, cfg, silentLogger())
	return dc, router, registry
}

// TestDigestPerSessionScopeFiltered is the ticket's named required check and
// R-21.227's worked example: a digest built for a Project1 session from
// accumulated Project1 AND Project3 items contains no Project3 DeepLink and
// no Project3 identifier -- only an opaque withheld count. It also pins the
// addressing that makes the filtering mean anything: the digest is ADDRESSED
// to the session it was built for, private, so the predicate itself keeps it
// away from every other session.
func TestDigestPerSessionScopeFiltered(t *testing.T) {
	dc, _, _ := newTestDigestCompiler(t, time.Unix(1000, 0), AwayConfig{Threshold: time.Minute, DigestUrgentDeepLinks: 10})

	drained := []Notification{
		{ID: "p1-urgent", Class: ClassScoped, TargetScope: "project-1", Priority: PriorityUrgent, DeepLink: "cascade://project-1/urgent-item"},
		{ID: "p1-normal", Class: ClassScoped, TargetScope: "project-1", Priority: PriorityNormal},
		{ID: "p3-urgent", Class: ClassScoped, TargetScope: "project-3", Priority: PriorityUrgent, DeepLink: "cascade://project-3/secret-item"},
		{ID: "p3-high", Class: ClassScoped, TargetScope: "project-3", Priority: PriorityHigh, CorrelationID: "project-3-episode"},
	}

	n, inScope := dc.buildFor(session("p1-sess", "project-1"), drained, "episode-1", "node-x")

	if n.Class != ClassAddressed || n.TargetSession != "p1-sess" || n.Visibility != VisibilityPrivate {
		t.Fatalf("digest addressing = %v/%q/%v, want addressed/p1-sess/private", n.Class, n.TargetSession, n.Visibility)
	}
	if len(inScope) != 2 || inScope[0] != 0 || inScope[1] != 1 {
		t.Fatalf("in-scope indices = %v, want the two project-1 items [0 1]", inScope)
	}

	raw := string(n.Payload)
	for _, leak := range []string{"p3-urgent", "p3-high", "project-3", "secret-item", "project-3-episode"} {
		if strings.Contains(raw, leak) {
			t.Fatalf("Project1 digest payload leaked Project3 identifier %q: %s", leak, raw)
		}
	}

	var payload digestPayload
	if err := json.Unmarshal(n.Payload, &payload); err != nil {
		t.Fatalf("payload not JSON: %v", err)
	}
	if payload.Withheld != 2 {
		t.Fatalf("Withheld = %d, want 2 (the two project-3 items)", payload.Withheld)
	}
	if len(payload.UrgentDeepLinks) != 1 || payload.UrgentDeepLinks[0] != "cascade://project-1/urgent-item" {
		t.Fatalf("UrgentDeepLinks = %v, want exactly the project-1 urgent link", payload.UrgentDeepLinks)
	}
}

// TestDigestNeverReachesAnotherProjectsSession is the leak the previous
// implementation shipped and its own tests enforced. Input: sessions s1
// (project-1) and s3 (project-3), one accumulated project-1 urgent item
// carrying a project-1 DeepLink. s3's subscriber must receive NOTHING of
// project-1: not the link, not the identifier, and not s1's digest.
func TestDigestNeverReachesAnotherProjectsSession(t *testing.T) {
	dc, router, registry := newTestDigestCompiler(t, time.Unix(1100, 0), DefaultAwayConfig())
	sub1, got1 := recordingSubscriber("sub-1", true)
	sub3, got3 := recordingSubscriber("sub-3", true)
	registry.Subscribe(sub1, session("s1", "project-1"))
	registry.Subscribe(sub3, session("s3", "project-3"))

	seedAccumulated(router, Notification{
		ID: "p1-urgent", Class: ClassScoped, TargetScope: "project-1",
		Priority: PriorityUrgent, DeepLink: "cascade://project-1/x",
	})

	if err := dc.Compile(context.Background(), "episode", "node-x"); err != nil {
		t.Fatalf("Compile: %v", err)
	}
	NewDispatcher(router.queues, registry, NewInbox(), fixedClock{now: time.Unix(1100, 0)}, silentLogger()).
		Drain(context.Background())

	if len(*got1) != 1 || (*got1)[0].ID != "episode:s1" {
		t.Fatalf("s1's subscriber got %+v, want exactly its own digest episode:s1", *got1)
	}
	if len(*got3) != 1 || (*got3)[0].ID != "episode:s3" {
		t.Fatalf("s3's subscriber got %+v, want exactly its own digest episode:s3", *got3)
	}
	raw := string((*got3)[0].Payload)
	for _, leak := range []string{"project-1", "cascade://", "p1-urgent"} {
		if strings.Contains(raw, leak) {
			t.Fatalf("s3's digest leaked %q from project-1: %s", leak, raw)
		}
	}
	var p3 digestPayload
	if err := json.Unmarshal((*got3)[0].Payload, &p3); err != nil {
		t.Fatalf("s3 payload not JSON: %v", err)
	}
	if p3.Withheld != 1 || len(p3.UrgentDeepLinks) != 0 {
		t.Fatalf("s3 payload = %+v, want one opaque withheld and no links", p3)
	}
}

// TestDigestIsOnePerSessionNotOnePerSubscriber: Registry.Snapshot is keyed by
// subscriber, so two surfaces attached to one session used to produce two
// identical digests for that session. One session gets one digest.
func TestDigestIsOnePerSessionNotOnePerSubscriber(t *testing.T) {
	dc, router, registry := newTestDigestCompiler(t, time.Unix(1200, 0), DefaultAwayConfig())
	cliSub, cliGot := recordingSubscriber("cli", true)
	tuiSub, tuiGot := recordingSubscriber("tui", true)
	registry.Subscribe(cliSub, session("s1", "project-1"))
	registry.Subscribe(tuiSub, session("s1", "project-1"))

	seedAccumulatedFromBus(router, "backup.result", "n1", "project-1")

	if err := dc.Compile(context.Background(), "ep", "node-x"); err != nil {
		t.Fatalf("Compile: %v", err)
	}
	NewDispatcher(router.queues, registry, NewInbox(), fixedClock{now: time.Unix(1200, 0)}, silentLogger()).
		Drain(context.Background())

	if len(*cliGot) != 1 || len(*tuiGot) != 1 {
		t.Fatalf("per-subscriber deliveries = cli:%d tui:%d, want 1 each (one digest for the one session)",
			len(*cliGot), len(*tuiGot))
	}
	if (*cliGot)[0].ID != "ep:s1" || (*tuiGot)[0].ID != "ep:s1" {
		t.Fatalf("digest IDs = %q/%q, want both to be the single ep:s1 digest", (*cliGot)[0].ID, (*tuiGot)[0].ID)
	}
}

// TestDigestSkipsARegistrationWithNoSessionID proves an unaddressable
// registration is skipped rather than producing an undeliverable digest, and
// that its items are re-queued rather than lost.
func TestDigestSkipsARegistrationWithNoSessionID(t *testing.T) {
	dc, router, registry := newTestDigestCompiler(t, time.Unix(1300, 0), DefaultAwayConfig())
	sub, got := recordingSubscriber("anon", true)
	registry.Subscribe(sub, CandidateSession{ScopeIDs: map[string]bool{"project-1": true}})

	seedAccumulatedFromBus(router, "backup.result", "n1", "project-1")

	if err := dc.Compile(context.Background(), "ep", "node-x"); err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if len(*got) != 0 {
		t.Fatalf("an unaddressable registration received %d digests, want 0", len(*got))
	}
	if len(router.queues.queues[PriorityNormal]) != 1 {
		t.Fatal("the accumulated original was not re-queued when no addressable session existed")
	}
}

// TestDigestUrgentDeepLinksCapped proves the [notify.digest_urgent_deeplinks]
// cap is enforced even when more in-scope Urgent items exist.
func TestDigestUrgentDeepLinksCapped(t *testing.T) {
	dc, _, _ := newTestDigestCompiler(t, time.Unix(1400, 0), AwayConfig{Threshold: time.Minute, DigestUrgentDeepLinks: 2})

	var drained []Notification
	for i := 0; i < 5; i++ {
		id := string(rune('a' + i))
		drained = append(drained, Notification{
			ID: id, Class: ClassScoped, TargetScope: "scope-1", Priority: PriorityUrgent, DeepLink: "cascade://" + id,
		})
	}
	n, _ := dc.buildFor(session("s1", "scope-1"), drained, "ep", "node-x")
	var p digestPayload
	if err := json.Unmarshal(n.Payload, &p); err != nil {
		t.Fatalf("payload not JSON: %v", err)
	}
	if len(p.UrgentDeepLinks) != 2 {
		t.Fatalf("UrgentDeepLinks len = %d, want the cap of 2", len(p.UrgentDeepLinks))
	}
}

// TestDigestSummarizeCountsAllPriorityLevels covers summarizeCounts' rendering
// across every Priority plus withheld, and its empty-result fallback text.
func TestDigestSummarizeCountsAllPriorityLevels(t *testing.T) {
	got := summarizeCounts(map[Priority]int{PriorityUrgent: 3, PriorityHigh: 12, PriorityNormal: 4, PriorityLow: 1}, 2)
	want := "3 Urgent, 12 High, 4 Normal, 1 Low, 2 withheld while away"
	if got != want {
		t.Fatalf("summarizeCounts = %q, want %q", got, want)
	}
	if empty := summarizeCounts(map[Priority]int{}, 0); empty != "no notifications while away" {
		t.Fatalf("summarizeCounts(empty) = %q", empty)
	}
	if name := priorityDisplayName(Priority(99)); name != "Unknown" {
		t.Fatalf("priorityDisplayName(99) = %q, want Unknown", name)
	}
}
