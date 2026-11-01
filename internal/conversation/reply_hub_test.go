package conversation

// Purpose: the ReplyHub's frames and its 60 s post-terminal grace (R17c N-10).

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

func TestReplyPublishesStreamEvents(t *testing.T) {
	f := newReplyFixture(t, newScriptDoor("Hel", "lo"), 4000)
	seedOwnedThread(t, f.store, "th", provider.SensitivityRestricted)
	user := seedReplyTurn(t, f.store, "th", RoleUser, OriginLocal, "", "q")
	id, err := f.replier.open(ownerCLICtx(), "th", user.ID)
	if err != nil {
		t.Fatal(err)
	}
	live, err := f.hub.Subscribe(context.Background(), id, ownerCaller())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.replier.Reply(ownerCLICtx(), ReplyRequest{StreamID: id}); err != nil {
		t.Fatal(err)
	}
	want := []string{
		`{"type":"delta","delta":"Hel","thread_id":"th"}`,
		`{"type":"delta","delta":"lo","thread_id":"th"}`,
		`{"type":"done","thread_id":"th"}`,
	}
	late, err := f.hub.Subscribe(context.Background(), id, ownerCaller()) // within the grace: full replay
	if err != nil {
		t.Fatal(err)
	}
	for _, frames := range []<-chan Frame{live, late} {
		var got []string
		for fr := range frames {
			b, _ := json.Marshal(fr)
			got = append(got, string(b))
		}
		if len(got) != len(want) {
			t.Fatalf("frames = %v, want %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("frame %d = %s, want %s", i, got[i], want[i])
			}
		}
	}
	h, _ := NewReplyHub(f.clock)
	sid, _ := h.open("th", "u", ownerCaller())
	if _, err := h.claim(sid, ownerCaller()); err != nil {
		t.Fatal(err)
	}
	h.publish(sid, "partial")
	h.finish(sid, cascade.New(cascade.KindUnavailable, "lane down"))
	frames, _ := h.Subscribe(context.Background(), sid, ownerCaller())
	if last := lastFrame(frames); last != (Frame{Type: FrameError, Kind: cascade.KindUnavailable.String(), ThreadID: "th"}) {
		t.Fatalf("error frame = %+v, want only the kind and thread", last)
	}
}

func TestReplyHubDropsStreamAfterGrace(t *testing.T) {
	clock := testkit.NewFrozenClock(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))
	h, err := NewReplyHub(clock)
	if err != nil {
		t.Fatal(err)
	}
	id, err := h.open("th", "u", ownerCaller())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.claim(id, ownerCaller()); err != nil {
		t.Fatal(err)
	}
	h.publish(id, "text")
	h.finish(id, nil)
	held := h.streams[id]
	clock.Advance(replyStreamGrace - time.Second)
	if _, err := h.Subscribe(context.Background(), id, ownerCaller()); err != nil {
		t.Fatalf("subscribe inside the grace: %v", err)
	}
	clock.Advance(time.Second)
	_, err = h.Subscribe(context.Background(), id, ownerCaller())
	wantSentinel(t, err, errStreamNotFound)
	if len(h.streams) != 0 || held.frames != nil {
		t.Fatalf("after the grace: %d streams, buffer %v; want the stream and its buffer released", len(h.streams), held.frames)
	}
	unclaimed, _ := h.open("th", "u", ownerCaller())
	clock.Advance(replyStreamTTL)
	_, err = h.claim(unclaimed, ownerCaller())
	wantSentinel(t, err, errStreamNotFound)
	if _, err := NewReplyHub(nil); err != errHubNilClock { //nolint:errorlint // identity
		t.Fatalf("NewReplyHub(nil) = %v", err)
	}
}
