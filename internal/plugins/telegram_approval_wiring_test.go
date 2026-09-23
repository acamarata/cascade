// Purpose: unit coverage for telegram_approval_wiring.go's adapter,
//
//	reusing this package's own fakeRPCDoer/fakePathProvider/missingSocketPath
//	(cascadepa_rpc_success_test.go, cascadepa_wiring_test.go) rather than
//	redeclaring them.
//
// SPORT: internal/plugins:telegram-approval-wiring (TEST) — P1-E23-W5-S48-T4.
package plugins

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/client"
	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/internal/runtime"
)

func TestTelegramApprovalService_ShowPending_PathResolutionFailure(t *testing.T) {
	wantErr := errors.New("boom: no home directory")
	s := newTelegramApprovalService(nil, time.Second, func() (runtime.PathProvider, error) {
		return nil, wantErr
	})
	_, err := s.ShowPending(context.Background(), "req-1")
	if !errors.Is(err, wantErr) {
		t.Errorf("ShowPending: err = %v, want it to wrap %v", err, wantErr)
	}
}

func TestTelegramApprovalService_ShowPending_TransportUnreachable(t *testing.T) {
	socket := missingSocketPath(t)
	s := newTelegramApprovalService(client.UnixDialer, time.Second, func() (runtime.PathProvider, error) {
		return fakePathProvider{socket: socket}, nil
	})
	_, err := s.ShowPending(context.Background(), "req-1")
	if err == nil {
		t.Fatal("ShowPending: err = nil, want a real transport error")
	}
}

// TestTelegramApprovalService_ShowPending_CanBridgeIsTheRealMatrix proves
// Bridgeable is computed by calling the REAL policy.RemoteApprovabilityMatrix
// over the wire-returned ActionClass — never a re-derived rule (R-16.60c).
func TestTelegramApprovalService_ShowPending_CanBridgeIsTheRealMatrix(t *testing.T) {
	for _, tc := range []struct {
		name  string
		class policy.ActionClass
		want  bool
	}{
		{"workspace mutation is bridgeable", policy.ClassWorkspaceMutation, true},
		{"local dev is bridgeable", policy.ClassLocalDev, true},
		{"read is not on the allow-list", policy.ClassRead, false},
		{"external side effect is not bridgeable", policy.ClassExternalSideEffect, false},
		{"destructive privileged is not bridgeable", policy.ClassDestructivePrivileged, false},
		{"the invalid zero class is not bridgeable", policy.ActionClass(0), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doer := &fakeRPCDoer{fill: func(out any) {
				res := out.(*policy.PendingEntry)
				*res = policy.PendingEntry{
					RequestID: "req-1", Summary: "write x",
					ExpiresAt: time.Now(), ActionClass: tc.class,
				}
			}}
			s := newTelegramApprovalService(nil, 0, nil)
			s.doer = doer

			got, err := s.ShowPending(context.Background(), "req-1")
			if err != nil {
				t.Fatalf("ShowPending: %v", err)
			}
			if doer.calledMethod != client.ApprovalShowMethod {
				t.Errorf("called method = %q, want %q", doer.calledMethod, client.ApprovalShowMethod)
			}
			if got.Bridgeable != tc.want {
				t.Errorf("Bridgeable(%s) = %v, want %v", tc.class, got.Bridgeable, tc.want)
			}
			if got.RequestID != "req-1" || got.Summary != "write x" {
				t.Errorf("ShowPending result = %+v, want the projected entry fields", got)
			}
		})
	}
}

func TestTelegramApprovalService_ShowPending_RPCErrorPropagates(t *testing.T) {
	wantErr := errors.New("policy: no pending approval has request id \"req-x\"")
	s := newTelegramApprovalService(nil, 0, nil)
	s.doer = &fakeRPCDoer{err: wantErr}
	_, err := s.ShowPending(context.Background(), "req-x")
	if !errors.Is(err, wantErr) {
		t.Errorf("ShowPending: err = %v, want it to wrap %v", err, wantErr)
	}
}

// paramCapturingDoer is a same-package rpcDoer that records the exact
// params value a call submitted, alongside the fakeRPCDoer's method
// capture. A local type rather than a change to fakeRPCDoer
// (cascadepa_rpc_success_test.go): that file is outside this ticket's
// files_scope, and this test needs nothing else fakeRPCDoer already does.
type paramCapturingDoer struct {
	calledMethod string
	calledParams any
}

func (d *paramCapturingDoer) Do(_ context.Context, method string, params, _ any) error {
	d.calledMethod, d.calledParams = method, params
	return nil
}

// TestTelegramApprovalService_Grant_SubmitsRequestIDAlone proves Grant
// calls approval.grant with ONLY the request id set (this file's own
// HONEST GAP header explains why — no signed token exists to load): the
// earlier version only checked the method name, never the params value
// itself (rework fix 8).
func TestTelegramApprovalService_Grant_SubmitsRequestIDAlone(t *testing.T) {
	doer := &paramCapturingDoer{}
	s := newTelegramApprovalService(nil, 0, nil)
	s.doer = doer
	if err := s.Grant(context.Background(), "req-2"); err != nil {
		t.Fatalf("Grant: %v", err)
	}
	if doer.calledMethod != client.ApprovalGrantMethod {
		t.Errorf("called method = %q, want %q", doer.calledMethod, client.ApprovalGrantMethod)
	}
	params, ok := doer.calledParams.(policy.ApprovalGrantParams)
	if !ok {
		t.Fatalf("called params = %#v (%T), want policy.ApprovalGrantParams", doer.calledParams, doer.calledParams)
	}
	if params.RequestID != "req-2" {
		t.Errorf("params.RequestID = %q, want %q", params.RequestID, "req-2")
	}
	if params.SignedToken != "" {
		t.Errorf("params.SignedToken = %q, want empty — Grant submits the request id alone", params.SignedToken)
	}
}

func TestTelegramApprovalService_Grant_PathResolutionFailure(t *testing.T) {
	wantErr := errors.New("boom: no home directory")
	s := newTelegramApprovalService(nil, time.Second, func() (runtime.PathProvider, error) {
		return nil, wantErr
	})
	if err := s.Grant(context.Background(), "req-2"); !errors.Is(err, wantErr) {
		t.Errorf("Grant: err = %v, want it to wrap %v", err, wantErr)
	}
}

func TestTelegramApprovalService_Grant_RPCErrorPropagates(t *testing.T) {
	wantErr := errors.New("policy: no approval verifier is enrolled, so no approval token can be honoured")
	s := newTelegramApprovalService(nil, 0, nil)
	s.doer = &fakeRPCDoer{err: wantErr}
	if err := s.Grant(context.Background(), "req-2"); !errors.Is(err, wantErr) {
		t.Errorf("Grant: err = %v, want it to wrap %v", err, wantErr)
	}
}

func TestTelegramApprovalService_Deny_CallsTheRealMethod(t *testing.T) {
	doer := &fakeRPCDoer{}
	s := newTelegramApprovalService(nil, 0, nil)
	s.doer = doer
	if err := s.Deny(context.Background(), "req-3"); err != nil {
		t.Fatalf("Deny: %v", err)
	}
	if doer.calledMethod != client.ApprovalDenyMethod {
		t.Errorf("called method = %q, want %q", doer.calledMethod, client.ApprovalDenyMethod)
	}
}

func TestTelegramApprovalService_Deny_PathResolutionFailure(t *testing.T) {
	wantErr := errors.New("boom: no home directory")
	s := newTelegramApprovalService(nil, time.Second, func() (runtime.PathProvider, error) {
		return nil, wantErr
	})
	if err := s.Deny(context.Background(), "req-3"); !errors.Is(err, wantErr) {
		t.Errorf("Deny: err = %v, want it to wrap %v", err, wantErr)
	}
}
