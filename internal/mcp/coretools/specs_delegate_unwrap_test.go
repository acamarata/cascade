package coretools

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// TestTypedDelegateErrorsKeepKindReachable proves a delegate tool's typed
// refusal still unwraps to the taxonomy error it came from, so callers
// that classify errors see the original kind behind the JSON body.
func TestTypedDelegateErrorsKeepKindReachable(t *testing.T) {
	spec := Spec{Method: "conductor.delegate", Capability: CapabilityAgentsDelegate}
	cause := cascade.New(cascade.KindNotFound, "no such delegation")
	h := typedDelegateErrors(spec, func(context.Context, []byte) ([]byte, error) { return nil, cause })
	_, err := h(context.Background(), nil)
	if err == nil {
		t.Fatal("handler error was swallowed")
	}
	if !strings.Contains(err.Error(), `"kind":"`+cascade.KindNotFound.String()+`"`) {
		t.Fatalf("body %q does not carry the not-found kind", err.Error())
	}
	var ce *cascade.Error
	if !errors.As(err, &ce) || ce != cause {
		t.Fatalf("errors.As reached %v, want the original cause", ce)
	}
}

// TestTypedDelegateErrorsLeaveOtherToolsAlone proves a tool outside the
// delegate capability keeps its own handler and error untouched.
func TestTypedDelegateErrorsLeaveOtherToolsAlone(t *testing.T) {
	want := errors.New("plain failure")
	spec := Spec{Method: "inbox.list", Capability: CapabilityInboxRead}
	h := typedDelegateErrors(spec, func(context.Context, []byte) ([]byte, error) { return nil, want })
	if _, err := h(context.Background(), nil); err != want {
		t.Fatalf("non-delegate error = %v, want the handler's own error", err)
	}
}
