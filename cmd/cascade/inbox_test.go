package main

// Purpose: covers `cascade inbox` (list, read, ack): the three verbs in
//   human, json and quiet modes against an in-memory stand-in for the
//   daemon's client, the daemon-down refusal through the real client dial
//   and the platform gate.
// Constraints: imports neither "net" nor "net/http", so it runs in the
//   no-network unit lane; the real-socket path is the dial in the
//   daemon-down test.
// SPORT: cli/inbox/ADD (P1-HRN-06c).

import (
	"context"
	goruntime "runtime"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/client"
	"github.com/acamarata/cascade/internal/notify"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/inboxenv"
)

// fakeInbox serves one two-page item, "item1", and refuses every other id
// the way the daemon does.
type fakeInbox struct{ acked []string }

var fakeEnvelope = inboxenv.Envelope{ID: "item1", Class: "review", OriginScope: "s", DataClass: "internal",
	Trust: "untrusted", Summary: "the first item"}

func (f *fakeInbox) InboxList(_ context.Context, p client.InboxListParams) (notify.InboxListResult, error) {
	res := notify.InboxListResult{Items: []inboxenv.Envelope{fakeEnvelope}, Total: 2, Unread: 1, Withheld: 1}
	if p.Unread && p.All {
		res.Total = 99
	}
	return res, nil
}

func (f *fakeInbox) InboxRead(_ context.Context, id string, offset int) (notify.InboxReadResult, error) {
	if id != "item1" {
		return notify.InboxReadResult{}, notify.ErrNotificationUnknown
	}
	res := notify.InboxReadResult{Envelope: fakeEnvelope, Heading: inboxenv.BlockHeading, Offset: offset,
		TotalBytes: 20, Body: "first page", NextOffset: 10}
	if offset > 0 {
		res.Body, res.NextOffset, res.Done = "second page", 20, true
	}
	return res, nil
}

func (f *fakeInbox) InboxAck(_ context.Context, id string) (notify.InboxAckResult, error) {
	if id != "item1" {
		return notify.InboxAckResult{}, notify.ErrNotificationUnknown
	}
	f.acked = append(f.acked, id)
	return notify.InboxAckResult{ID: id, Acked: true}, nil
}

func fakeInboxDeps(f *fakeInbox) inboxDeps {
	return inboxDeps{Open: func(context.Context) (inboxAPI, error) { return f, nil }}
}

func runInbox(t *testing.T, mk func(inboxDeps) *cobra.Command, deps inboxDeps, args ...string) (string, error) {
	t.Helper()
	return execLeaf(t, mk(deps), args)
}

func TestInboxCLI(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("the inbox noun is unsupported on windows")
	}
	f := &fakeInbox{}
	deps := fakeInboxDeps(f)
	human, err := runInbox(t, newInboxListCmd, deps)
	if err != nil || !strings.Contains(human, "item1") || !strings.Contains(human, "2 total, 1 unread, 1 withheld") {
		t.Fatalf("human list = %q, %v", human, err)
	}
	quiet, err := runInbox(t, newInboxListCmd, deps, "--quiet")
	if err != nil || quiet != human {
		t.Fatalf("quiet list = %q, %v; want the human output %q", quiet, err, human)
	}
	js, err := runInbox(t, newInboxListCmd, deps, "--json", "--unread")
	if err != nil || !strings.Contains(js, `"ok": true`) || !strings.Contains(js, `"items"`) {
		t.Fatalf("json list = %q, %v", js, err)
	}
	if all, _ := runInbox(t, newInboxListCmd, deps, "--all", "--unread"); !strings.Contains(all, "99 total") {
		t.Errorf("--all and --unread did not reach the daemon: %q", all)
	}
	assertInboxRead(t, deps)
	assertInboxAck(t, deps, f)
}

func assertInboxRead(t *testing.T, deps inboxDeps) {
	t.Helper()
	first, err := runInbox(t, newInboxReadCmd, deps, "item1")
	if err != nil || !strings.Contains(first, "first page") || !strings.Contains(first, "--page 1") ||
		!strings.Contains(first, inboxenv.BlockHeading) {
		t.Fatalf("first page = %q, %v", first, err)
	}
	second, err := runInbox(t, newInboxReadCmd, deps, "item1", "--page", "1")
	if err != nil || !strings.Contains(second, "second page") || strings.Contains(second, "more follows") {
		t.Fatalf("second page = %q, %v", second, err)
	}
	js, err := runInbox(t, newInboxReadCmd, deps, "item1", "--json", "--page", "1")
	if err != nil || !strings.Contains(js, `"page": 1`) || !strings.Contains(js, `"done": true`) {
		t.Fatalf("json read = %q, %v", js, err)
	}
	if _, err := runInbox(t, newInboxReadCmd, deps, "item1", "--page", "5"); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Errorf("a page past the end = %v, want invalid input", err)
	}
	if _, err := runInbox(t, newInboxReadCmd, deps, "nope"); !cascade.HasKind(err, cascade.KindNotFound) {
		t.Errorf("an unknown id = %v, want the daemon's not-found kept", err)
	}
}

func assertInboxAck(t *testing.T, deps inboxDeps, f *fakeInbox) {
	t.Helper()
	out, err := runInbox(t, newInboxAckCmd, deps, "item1")
	if err != nil || !strings.Contains(out, "acknowledged item1") || len(f.acked) != 1 {
		t.Fatalf("ack = %q, %v, acked %v", out, err, f.acked)
	}
	if js, _ := runInbox(t, newInboxAckCmd, deps, "item1", "--json"); !strings.Contains(js, `"acked": true`) {
		t.Errorf("json ack = %q", js)
	}
	if _, err := runInbox(t, newInboxAckCmd, deps, "nope"); !cascade.HasKind(err, cascade.KindNotFound) {
		t.Errorf("ack of an unknown id = %v, want not found", err)
	}
}

func TestInboxCLIDaemonDownRefuses(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("windows refuses earlier, with the unsupported kind")
	}
	t.Setenv("CASCADE_NO_INPUT", "1")
	deps := inboxDeps{Paths: fakeDaemonPaths{root: t.TempDir()}, DialContext: client.UnixDialer}
	cases := []struct {
		name string
		mk   func(inboxDeps) *cobra.Command
		args []string
	}{
		{"list", newInboxListCmd, nil},
		{"read", newInboxReadCmd, []string{"item1"}},
		{"ack", newInboxAckCmd, []string{"item1"}},
	}
	for _, tc := range cases {
		out, err := runInbox(t, tc.mk, deps, tc.args...)
		if !cascade.HasKind(err, cascade.KindUnavailable) {
			t.Fatalf("inbox %s = %v (%q), want unavailable", tc.name, err, out)
		}
		if !strings.Contains(err.Error(), "the daemon is not running") || strings.Contains(err.Error(), "unreachable") {
			t.Errorf("inbox %s message = %q, want the fixed refusal", tc.name, err.Error())
		}
	}
}

func TestInboxCLIWindowsUnsupported(t *testing.T) {
	err := inboxPlatformGate("windows")
	if !cascade.HasKind(err, cascade.KindUnsupported) {
		t.Fatalf("windows gate = %v, want unsupported", err)
	}
	for _, goos := range []string{"darwin", "linux"} {
		if err := inboxPlatformGate(goos); err != nil {
			t.Errorf("%s gate = %v, want nil", goos, err)
		}
	}
	if goruntime.GOOS != "windows" {
		return
	}
	if _, err := runInbox(t, newInboxListCmd, productionInboxDeps()); !cascade.HasKind(err, cascade.KindUnsupported) {
		t.Errorf("inbox list on windows = %v, want unsupported", err)
	}
}
