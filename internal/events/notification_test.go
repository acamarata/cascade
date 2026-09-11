package events_test

import (
	"reflect"
	"testing"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/pkg/cascade"
)

func TestNotificationPayloadRoundTrip(t *testing.T) {
	in := events.NotificationPayload{
		ID:            "n1",
		DeepLink:      "cascade://inbox/n1",
		TargetScope:   "project-1",
		Visibility:    "scoped",
		CorrelationID: "corr-1",
	}
	encoded := events.EncodeNotificationPayload(in)
	got, err := events.DecodeNotificationPayload(encoded)
	if err != nil {
		t.Fatalf("DecodeNotificationPayload: %v", err)
	}
	if !reflect.DeepEqual(got, in) {
		t.Fatalf("round trip mismatch: got %+v, want %+v", got, in)
	}
}

func TestNotificationPayloadDecodeMalformedNeverPanics(t *testing.T) {
	cases := [][]byte{
		nil,
		[]byte(""),
		[]byte("{"),
		[]byte("not json"),
		[]byte(`{"id": 5}`), // wrong type for id
	}
	for _, c := range cases {
		_, err := events.DecodeNotificationPayload(c)
		if err == nil {
			t.Fatalf("DecodeNotificationPayload(%q) = nil error, want cascade.KindIntegrity", c)
		}
		if k, ok := cascade.KindOf(err); !ok || k != cascade.KindIntegrity {
			t.Fatalf("DecodeNotificationPayload(%q) kind mismatch", c)
		}
	}
}

func TestNotificationPayloadDecodeMissingID(t *testing.T) {
	_, err := events.DecodeNotificationPayload([]byte(`{}`))
	if err == nil {
		t.Fatal("missing id: want error")
	}
	if k, ok := cascade.KindOf(err); !ok || k != cascade.KindIntegrity {
		t.Fatalf("missing id: err = %v, want KindIntegrity", err)
	}
}
