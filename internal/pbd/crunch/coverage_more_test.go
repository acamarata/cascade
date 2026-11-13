package crunch

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

// TestPopulateGCAuthLaterEvidenceInvalid checks that a bad later entry fails
// even when earlier entries are valid.
func TestPopulateGCAuthLaterEvidenceInvalid(t *testing.T) {
	est := func([]GCQuotaEvidence) (time.Time, string, error) { return time.Time{}, "", nil }
	ev := []GCQuotaEvidence{{Usage: 1, Capacity: 2}, {Usage: 3, Capacity: 2}}
	got, err := PopulateGCAuth(GCTicket{ID: "x"}, ev, est)
	if !errors.Is(err, ErrGCAuthFields) {
		t.Fatalf("err = %v, want ErrGCAuthFields", err)
	}
	if !reflect.DeepEqual(got, GCAuth{}) {
		t.Fatalf("got %+v, want zero GCAuth", got)
	}
}

// TestInjectReseedRecapPending covers the reseedPending=true branch.
func TestInjectReseedRecapPending(t *testing.T) {
	hist := []HistoryItem{{Role: "user", Text: "a"}, {Role: "assistant", Text: "b"}}
	cases := []struct {
		name    string
		payload CodePayload
		history []HistoryItem
	}{
		{"absent recap", CodePayload{Text: "t"}, hist},
		{"preexisting recap", CodePayload{Text: "t", ReseedRecap: ReseedRecap{{Role: "user", Text: "old"}}}, hist},
		{"nil history", CodePayload{Text: "t"}, nil},
		{"over limit history", CodePayload{Text: "t"}, make([]HistoryItem, M1286RecapLimit+2)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// pending=true replaces the recap with the latest
			// min(len(history), M1286RecapLimit) items, nil when history is empty.
			want := CodePayload{Text: tc.payload.Text}
			if n := min(len(tc.history), M1286RecapLimit); n > 0 {
				want.ReseedRecap = append(ReseedRecap(nil), tc.history[len(tc.history)-n:]...)
			}
			got := InjectReseedRecap(tc.payload, true, tc.history)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("got %#v, want %#v", got, want)
			}
		})
	}
}

// TestVerifyWithNodesValidInputs covers the success branch with varied inputs.
func TestVerifyWithNodesValidInputs(t *testing.T) {
	cases := []struct {
		name  string
		nodes []string
		cmds  []string
		v     NodeVerifier
	}{
		{"nil nodes", nil, []string{"go test"}, &fakeVerifier{}},
		{"many commands", []string{"a", "b"}, []string{"c1", "c2", "c3"}, &fakeVerifier{freeNodes: []string{"a"}}},
		{"verifier errors ignored", []string{"a"}, []string{"c"}, &fakeVerifier{freeErr: ErrVerifyNodes, verifyErr: ErrVerifyNodes}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := VerifyWithNodes(tc.nodes, tc.cmds, tc.v)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != (VerifyTarget{}) {
				t.Fatalf("got %+v, want zero target", got)
			}
		})
	}
}

// TestVerifyWithNodesNilCommands covers a nil commands slice with a verifier.
func TestVerifyWithNodesNilCommands(t *testing.T) {
	got, err := VerifyWithNodes(nil, nil, &fakeVerifier{})
	if !errors.Is(err, ErrVerifyNodes) {
		t.Fatalf("err = %v, want ErrVerifyNodes", err)
	}
	if got != (VerifyTarget{}) {
		t.Fatalf("got %+v, want zero target", got)
	}
}
