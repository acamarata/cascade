package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"syscall"
	"testing"

	"github.com/acamarata/cascade/internal/client"
	"github.com/acamarata/cascade/internal/doctor"
	"github.com/acamarata/cascade/pkg/cascade"
)

// TestNotifyDropsDoctorCheck exercises RPC and decoding without a socket.
func TestNotifyDropsDoctorCheck(t *testing.T) {
	for _, tc := range []struct {
		name, result string
		err          error
		status       doctor.Status
		message      string
	}{
		{name: "zero", result: `{"dropped":{},"accumulated":0}`, status: doctor.StatusOK, message: "no notifications dropped"},
		{name: "counts", result: `{"dropped":{"queue-full":2,"accumulation-full":3},"accumulated":4}`, status: doctor.StatusWarn, message: "accumulation-full=3, queue-full=2"},
		{name: "zero reason", result: `{"dropped":{"queue-full":0},"accumulated":0}`, status: doctor.StatusOK},
		{name: "down missing", err: fmt.Errorf("dial: %w", fs.ErrNotExist), status: doctor.StatusWarn, message: "daemon not running"},
		{name: "down refused", err: fmt.Errorf("dial: %w", syscall.ECONNREFUSED), status: doctor.StatusWarn, message: "daemon not running"},
		{name: "down dial failed", err: fmt.Errorf("dial: %w", tooLongSocketDialErr()), status: doctor.StatusWarn, message: "daemon not running"},
		{name: "RPC unavailable", err: cascade.New(cascade.KindUnavailable, "private detail"), status: doctor.StatusError, message: "notify.stats: RPC error"},
		{name: "RPC refused", err: cascade.New(cascade.KindPermissionDenied, "private detail"), status: doctor.StatusError, message: "notify.stats: RPC error"},
		{name: "timeout", err: context.DeadlineExceeded, status: doctor.StatusError, message: "notify.stats: RPC error"},
		{name: "canceled", err: context.Canceled, status: doctor.StatusError},
		{name: "malformed", result: `{`, status: doctor.StatusError, message: "decode error"},
		{name: "trailing data", result: `{"dropped":{},"accumulated":0} {}`, status: doctor.StatusError},
		{name: "extra fields", result: `{"dropped":{},"accumulated":0,"payload":"private detail"}`, status: doctor.StatusError},
		{name: "wrong counter", result: `{"dropped":{"queue-full":"private detail"},"accumulated":0}`, status: doctor.StatusError},
		{name: "negative counter", result: `{"dropped":{"queue-full":-1},"accumulated":0}`, status: doctor.StatusError},
		{name: "missing", result: `{}`, status: doctor.StatusError},
		{name: "null", result: `null`, status: doctor.StatusError},
		{name: "missing accumulation", result: `{"dropped":{}}`, status: doctor.StatusError},
		{name: "negative accumulation", result: `{"dropped":{},"accumulated":-1}`, status: doctor.StatusError},
		{name: "unknown reason", result: `{"dropped":{"private detail":1},"accumulated":0}`, status: doctor.StatusError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			check := notifyDropsCheck{call: func(_ context.Context, method string, params, out any) error {
				calls++
				if method != "notify.stats" || params != nil {
					t.Fatalf("unexpected RPC %q with params", method)
				}
				*out.(*json.RawMessage) = json.RawMessage(tc.result)
				return tc.err
			}}
			got, err := check.Run(context.Background())
			if err != nil || calls != 1 || got.Status != tc.status || !strings.Contains(got.Message, tc.message) {
				t.Fatalf("Run = %+v, %v; calls=%d, want %s containing %q", got, err, calls, tc.status, tc.message)
			}
			if strings.Contains(fmt.Sprint(got), "private detail") {
				t.Fatal("diagnostic disclosed RPC contents")
			}
		})
	}
}

// TestNotifyDropsDoctorContract checks the registry-facing methods and nil caller.
func TestNotifyDropsDoctorContract(t *testing.T) {
	c := notifyDropsCheck{}
	if c.Name() != "notify-drops" || c.Describe() == "" || c.Metadata().Fixable {
		t.Fatal("invalid check metadata")
	}
	if _, err := c.Fix(context.Background()); !errors.Is(err, doctor.ErrCheckNotFixable) {
		t.Fatalf("Fix = %v", err)
	}
	got, err := c.Run(context.Background())
	if err != nil || got.Status != doctor.StatusError {
		t.Fatalf("nil caller = %+v, %v", got, err)
	}
}

// tooLongSocketDialErr is the dial error UnixDialer returns for a socket path
// past the platform limit, as on a CI runner whose temp HOME is deep.
func tooLongSocketDialErr() error {
	_, err := client.UnixDialer(context.Background(), "/"+strings.Repeat("d", 200)+"/daemon.sock")
	return err
}
