// Purpose: unit coverage for cascadepa_topics_wiring.go's adapter,
//
//	mirroring cascadepa_wiring_test.go's own isolation posture (a fake
//	pathResolver, a real client.UnixDialer against a socket nothing
//	binds -- never "net" named directly in this _test.go file, per
//	internal/build's no-network-unit-lane gate).
//
// SPORT: internal/plugins:cascadepa-topics-wiring (TEST) -- P1-E21-W5-S46-T4.
package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/client"
	"github.com/acamarata/cascade/internal/conversation"
	"github.com/acamarata/cascade/internal/runtime"
	pacmd "github.com/acamarata/cascade/plugins/cascade-pa/cmd"
)

func TestCascadePATopicsClient_ListTopics_PathResolutionFailure(t *testing.T) {
	wantErr := errors.New("boom: no home directory")
	c := newCascadePATopicsClient(nil, time.Second, func() (runtime.PathProvider, error) {
		return nil, wantErr
	})
	_, err := c.ListTopics(context.Background())
	if err == nil {
		t.Fatal("ListTopics: err = nil, want a path-resolution error")
	}
	if !errors.Is(err, wantErr) {
		t.Errorf("ListTopics: err = %v, want it to wrap %v", err, wantErr)
	}
}

func TestCascadePATopicsClient_ListTopics_TransportUnreachable(t *testing.T) {
	socket := missingSocketPath(t)
	c := newCascadePATopicsClient(client.UnixDialer, time.Second, func() (runtime.PathProvider, error) {
		return fakePathProvider{socket: socket}, nil
	})
	_, err := c.ListTopics(context.Background())
	if err == nil {
		t.Fatal("ListTopics: err = nil, want a transport error")
	}
	if got := err.Error(); !containsAll(got, "chat.topics_list", "daemon not running or unreachable") {
		t.Errorf("ListTopics: err = %q, want internal/client's own classified transport error", got)
	}
}

func TestCascadePATopicsClient_ListThreads_TransportUnreachable(t *testing.T) {
	socket := missingSocketPath(t)
	c := newCascadePATopicsClient(client.UnixDialer, time.Second, func() (runtime.PathProvider, error) {
		return fakePathProvider{socket: socket}, nil
	})
	_, err := c.ListThreads(context.Background(), pacmd.ThreadsListRequest{Page: 1, PageSize: 10})
	if err == nil {
		t.Fatal("ListThreads: err = nil, want a transport error")
	}
	if got := err.Error(); !containsAll(got, "chat.threads_list", "daemon not running or unreachable") {
		t.Errorf("ListThreads: err = %q, want internal/client's own classified transport error", got)
	}
}

func TestCascadePATopicsClient_OpenThread_TransportUnreachable(t *testing.T) {
	socket := missingSocketPath(t)
	c := newCascadePATopicsClient(client.UnixDialer, time.Second, func() (runtime.PathProvider, error) {
		return fakePathProvider{socket: socket}, nil
	})
	_, err := c.OpenThread(context.Background(), "my-thread")
	if err == nil {
		t.Fatal("OpenThread: err = nil, want a transport error")
	}
	// OpenThread dials the EXISTING chat.get_thread door (this file's
	// header), never a fabricated third method -- pinned here so a future
	// edit that invents one is caught immediately.
	if got := err.Error(); !containsAll(got, "chat.get_thread", "daemon not running or unreachable") {
		t.Errorf("OpenThread: err = %q, want a chat.get_thread transport error", got)
	}
}

// TestCascadePATopicsClient_ProjectionsUseTopicTypeForSlugAndTitle proves
// the wire projection this file's ListThreads doc comment describes,
// against a fake rpcDoer (no socket at all) -- both Slug and Title come
// from TopicType, the only real, non-fabricated label the topic engine's
// 1-thread-per-type ThreadStore design offers (thread_store.go).
func TestCascadePATopicsClient_ProjectionsUseTopicTypeForSlugAndTitle(t *testing.T) {
	c := &cascadePATopicsClient{doer: fakeDoer{
		result: threadsListResultWire{
			Threads:    []threadSummaryWireClient{{TopicType: "code", ID: "topic:code", TurnCount: 4}},
			Page:       1,
			PageSize:   20,
			TotalCount: 1,
		},
	}}
	got, err := c.ListThreads(context.Background(), pacmd.ThreadsListRequest{Page: 1, PageSize: 20})
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	if len(got.Threads) != 1 || got.Threads[0].Slug != "code" || got.Threads[0].Title != "code" {
		t.Fatalf("Threads = %+v, want Slug=Title=\"code\"", got.Threads)
	}
	if got.Threads[0].MessageCount != 4 {
		t.Fatalf("MessageCount = %d, want 4", got.Threads[0].MessageCount)
	}
}

// fakeDoer is a minimal rpcDoer test double that decodes its fixed result
// into out via a JSON round trip, so it exercises the same decode path
// the real transport does.
type fakeDoer struct{ result any }

func (f fakeDoer) Do(_ context.Context, _ string, _, out any) error {
	b, err := json.Marshal(f.result)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}

// TestCascadePATopicsClient_ListTopics_Success exercises ListTopics'
// success path against a fakeDoer (no socket at all) -- the projection
// loop and its make/append/return statements are otherwise only reached
// through a live daemon, which this package's isolation posture forbids.
func TestCascadePATopicsClient_ListTopics_Success(t *testing.T) {
	c := &cascadePATopicsClient{doer: fakeDoer{
		result: topicsListWire{
			Topics: []topicSummaryWireClient{
				{TopicType: "code", ThreadCount: 3, TurnCount: 10},
				{TopicType: "ops", ThreadCount: 1, TurnCount: 2},
			},
		},
	}}
	got, err := c.ListTopics(context.Background())
	if err != nil {
		t.Fatalf("ListTopics: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("ListTopics: len = %d, want 2", len(got))
	}
	if got[0].Label != "code" || got[0].ThreadCount != 3 {
		t.Errorf("ListTopics[0] = %+v, want Label=code ThreadCount=3", got[0])
	}
	if got[1].Label != "ops" || got[1].ThreadCount != 1 {
		t.Errorf("ListTopics[1] = %+v, want Label=ops ThreadCount=1", got[1])
	}
}

// TestCascadePATopicsClient_ListTopics_EmptyResult proves the projection
// loop's zero-iteration path returns a non-nil, zero-length slice rather
// than nil -- pinned separately from the non-empty success test above so
// a future edit that special-cases "no topics" into a nil return is
// caught here, not by a flaky len()==0-passes-either-way assertion.
func TestCascadePATopicsClient_ListTopics_EmptyResult(t *testing.T) {
	c := &cascadePATopicsClient{doer: fakeDoer{result: topicsListWire{}}}
	got, err := c.ListTopics(context.Background())
	if err != nil {
		t.Fatalf("ListTopics: %v", err)
	}
	if got == nil {
		t.Fatal("ListTopics: got = nil, want a non-nil empty slice")
	}
	if len(got) != 0 {
		t.Fatalf("ListTopics: len = %d, want 0", len(got))
	}
}

// TestCascadePATopicsClient_ListThreads_PathResolutionFailure mirrors
// TestCascadePATopicsClient_ListTopics_PathResolutionFailure for
// ListThreads' own rpcClient() call -- the existing
// TestCascadePATopicsClient_ListThreads_TransportUnreachable test only
// exercises the path where resolvePaths succeeds and the dial fails, so
// the resolvePaths-itself-fails branch was unreached.
func TestCascadePATopicsClient_ListThreads_PathResolutionFailure(t *testing.T) {
	wantErr := errors.New("boom: no home directory")
	c := newCascadePATopicsClient(nil, time.Second, func() (runtime.PathProvider, error) {
		return nil, wantErr
	})
	_, err := c.ListThreads(context.Background(), pacmd.ThreadsListRequest{Page: 1, PageSize: 10})
	if err == nil {
		t.Fatal("ListThreads: err = nil, want a path-resolution error")
	}
	if !errors.Is(err, wantErr) {
		t.Errorf("ListThreads: err = %v, want it to wrap %v", err, wantErr)
	}
}

// TestCascadePATopicsClient_OpenThread_PathResolutionFailure mirrors the
// same gap for OpenThread's rpcClient() call.
func TestCascadePATopicsClient_OpenThread_PathResolutionFailure(t *testing.T) {
	wantErr := errors.New("boom: no home directory")
	c := newCascadePATopicsClient(nil, time.Second, func() (runtime.PathProvider, error) {
		return nil, wantErr
	})
	_, err := c.OpenThread(context.Background(), "my-thread")
	if err == nil {
		t.Fatal("OpenThread: err = nil, want a path-resolution error")
	}
	if !errors.Is(err, wantErr) {
		t.Errorf("OpenThread: err = %v, want it to wrap %v", err, wantErr)
	}
}

// TestCascadePATopicsClient_OpenThread_Success exercises OpenThread's
// success path against a fakeDoer, proving the projection this file's
// header describes: Slug comes from the requested slug (the topic
// engine's ThreadStore has no separate slug field), Title from
// Thread.Name, and MessageCount from len(Turns).
func TestCascadePATopicsClient_OpenThread_Success(t *testing.T) {
	c := &cascadePATopicsClient{doer: fakeDoer{
		result: getThreadResultWire{
			Thread: conversation.Thread{ID: "topic:code", Name: "code"},
			Turns: []turnWithSegsWire{
				{Turn: conversation.Turn{ID: "t1"}},
				{Turn: conversation.Turn{ID: "t2"}},
			},
		},
	}}
	got, err := c.OpenThread(context.Background(), "code")
	if err != nil {
		t.Fatalf("OpenThread: %v", err)
	}
	if got.ID != "topic:code" || got.Slug != "code" || got.Title != "code" {
		t.Errorf("OpenThread = %+v, want ID=topic:code Slug=Title=code", got)
	}
	if got.MessageCount != 2 {
		t.Errorf("OpenThread.MessageCount = %d, want 2", got.MessageCount)
	}
}
