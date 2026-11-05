package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

// TestNotifyDroppedEventHasNoPayload checks the complete typed sink event,
// including fields added in the future, rather than searching for one body.
func TestNotifyDroppedEventHasNoPayload(t *testing.T) {
	_, queues, _, inbox := newTestDispatcher(t, time.Unix(100, 0))
	var published []DropEvent
	queues.drops.setSink(func(_ context.Context, e DropEvent) {
		if _, err := inbox.Read(context.Background(), session("a"), e.ID); err != nil {
			t.Fatalf("sink ran before inbox record: %v", err)
		}
		published = append(published, e)
	})
	n := Notification{ID: "dropped", Source: "producer", Priority: PriorityNormal,
		Class: ClassGlobalCritical, TargetSession: "a", Visibility: VisibilityPrivate,
		Payload: []byte("private notification body")}
	for i := 0; i < cap(queues.queues[PriorityNormal]); i++ {
		queues.enqueue(Notification{ID: "queued", Priority: PriorityNormal})
	}
	queues.enqueue(n)
	if len(published) != 1 {
		t.Fatalf("sink calls = %d, want 1", len(published))
	}
	data, err := json.Marshal(published[0])
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		ID, Source, Reason string
		Priority           Priority
		At                 time.Time
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		t.Fatalf("drop event has unexpected fields: %v", err)
	}
	want := DropEvent{ID: n.ID, Source: n.Source, Reason: DropQueueFull,
		Priority: n.Priority, At: time.Unix(100, 0)}
	if !reflect.DeepEqual(DropEvent(decoded), want) {
		t.Fatal("drop event metadata differs from the dropped notification")
	}
	if bytes.Contains(data, n.Payload) {
		t.Fatal("drop event contains the notification body")
	}
}
