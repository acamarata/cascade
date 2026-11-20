package economics

// Purpose: RefreshGrantOnce against injected fake copies, token exchange,
// census and lane writer (every failure is injected here, in the test).

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
)

var (
	primaryDir = filepath.Join(os.TempDir(), "home", "op", ".harness")
	fleetDir   = filepath.Join(os.TempDir(), "fleet", "acct-2")
	poll5      = 5 * time.Minute
)

var (
	grantT0     = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	errBadEntry = errors.New("test: the entry does not decode")
	errDenied   = errors.New("test: permission denied on the realpath copy")
	rotated     = pairIn("acc-2", "ref-2", 8*time.Hour)
)

// testCodec stores a Credential as JSON (the fake vendor entry shape).
type testCodec struct{}

func (testCodec) Decode(b []byte) (c Credential, err error) {
	if json.Unmarshal(b, &c) != nil {
		return Credential{}, errBadEntry
	}
	return c, nil
}

func (testCodec) Encode(_ []byte, c Credential) ([]byte, error) { return json.Marshal(c) }

// rig is one account and its fake HarnessCredentials (log records each copy
// read; Read, a nil embedded method, would panic). Its exchange answers answer
// (a Credential, an error, or nil for invalid_grant) to tokens not "dead-*".
type rig struct {
	secrets.HarnessCredentials
	copies             []secrets.CopyEntry
	readErr            map[string]error
	log, posted        []string
	writes, lanes, ran int
	answer             any
	events             []AuthRefreshEvent
	live               bool
	idle, poll         time.Duration
	refreshers         []string
	onRun              func()
	memo               GrantMemo
	err                error
}

func (r *rig) ReadCopies(context.Context, string) ([]secrets.CopyEntry, error) {
	for _, c := range r.copies {
		if r.log = append(r.log, c.Label); r.readErr[c.Label] != nil {
			return nil, r.readErr[c.Label]
		}
	}
	return slices.Clone(r.copies), nil
}

func (r *rig) WriteDurable(_ context.Context, _ string, e secrets.Entry) error {
	for i := range r.copies {
		r.copies[i].Entry = e
	}
	r.writes++
	return nil
}

func newRig(poll time.Duration, answer any, copies ...secrets.CopyEntry) *rig {
	return &rig{copies: copies, answer: answer, idle: time.Hour, poll: poll, onRun: func() {}}
}

// copyOf is a copy holding c (JSON of a Credential never fails to encode).
func copyOf(label string, c Credential) secrets.CopyEntry {
	data, _ := json.Marshal(c)
	e, _ := secrets.NewEntry(data)
	return secrets.CopyEntry{Label: label, Entry: e}
}

func pairIn(access, refresh string, in time.Duration) Credential {
	return Credential{Access: []byte(access), Refresh: []byte(refresh), Expiry: grantT0.Add(in)}
}

// oneCopy is a client copy holding a full pair, left before its expiry.
func oneCopy(left time.Duration) secrets.CopyEntry {
	return copyOf("client", pairIn("acc-1", "ref-1", left))
}

func (r *rig) exchange(_ context.Context, refresh []byte) (Credential, error) {
	r.posted = append(r.posted, string(refresh))
	if c, ok := r.answer.(Credential); ok && !bytes.HasPrefix(refresh, []byte("dead")) {
		return c, nil
	} else if err, ok := r.answer.(error); ok {
		return Credential{}, err
	}
	return Credential{}, cascade.Wrap(cascade.KindPermissionDenied, secrets.ErrInvalidGrant, "test: rejected grant")
}

func (r *rig) deps() GrantDeps {
	return GrantDeps{
		Creds: r, Codec: testCodec{}, Clock: runtime.NewFixedClock(grantT0), DefaultDir: primaryDir,
		Exchange: r.exchange, IdleBeforeGrant: 10 * time.Minute, Refreshers: r.refreshers, Memo: &r.memo,
		Census:       func(context.Context, AccountRef) (bool, time.Duration, error) { return r.live, r.idle, nil },
		UsagePoll:    func() time.Duration { return r.poll },
		AuthRequired: func(context.Context, AccountRef) error { r.lanes++; return nil },
		Publish:      func(_ context.Context, ev AuthRefreshEvent) error { r.events = append(r.events, ev); return nil },
		RunRefresher: func(context.Context, string, string) error { r.ran++; r.onRun(); return nil },
		copies: func(string) (refs []secrets.CopyRef, _ error) {
			for _, c := range r.copies {
				refs = append(refs, secrets.CopyRef{Label: c.Label})
			}
			return refs, nil
		},
	}
}

// tick runs one grant tick for dir (primaryDir is PRIMARY); r.err keeps the error.
func (r *rig) tick(dir string) (res GrantResult) {
	res, r.err = RefreshGrantOnce(context.Background(), r.deps(), AccountRef{ID: "acct", ConfigDir: dir})
	return res
}

// holds reports whether every copy reads back exactly want (a full pair).
func (r *rig) holds(want Credential) bool {
	data, err := json.Marshal(want)
	return err == nil && len(want.Access) > 0 && len(want.Refresh) > 0 && !slices.ContainsFunc(r.copies,
		func(c secrets.CopyEntry) bool { return !bytes.Equal(c.Entry.Bytes(), data) })
}

func posts(want bool) int { return map[bool]int{true: 1}[want] }

func TestRefreshFallsBackToCopyWithRefreshToken(t *testing.T) {
	for left, due := range map[time.Duration]bool{30 * time.Minute: true, 5 * time.Hour: false} {
		r := newRig(poll5, rotated, copyOf("client", Credential{}), copyOf("realpath", pairIn("acc-1", "ref-1", left)))
		res := r.tick(primaryDir)
		if r.err != nil || res.Granted != due || res.Synced == due || res.AuthRequired || r.lanes != 0 || len(r.posted) != posts(due) {
			t.Fatalf("%s left: want grant=%v (else a sync), got %+v posted %v err %v", left, due, res, r.posted, r.err)
		}
		if due && !slices.Equal(r.posted, []string{"ref-1"}) || !r.holds(map[bool]Credential{true: rotated, false: pairIn("acc-1", "ref-1", left)}[due]) ||
			!slices.Equal(r.log, []string{"client", "realpath"}) {
			t.Fatalf("%s left: want the realpath token posted once, every copy synced, only the copy set read; posted %v log %v", left, r.posted, r.log)
		}
	}
}

func TestAuthRequiredWhenEveryCopyDead(t *testing.T) {
	for reason, tokens := range map[string][2]string{"no_refresh_token": {"", ""}, "invalid_grant": {"dead-1", "dead-2"}} {
		r := newRig(poll5, rotated, copyOf("client", pairIn("acc-1", tokens[0], 20*time.Minute)),
			copyOf("realpath", pairIn("acc-2", tokens[1], 10*time.Minute)))
		if res := r.tick(primaryDir); r.err != nil || !res.AuthRequired || res.Reason != reason || r.lanes != 1 || r.writes != 0 {
			t.Fatalf("%s: want auth-required with copies unchanged, got %+v lanes %d err %v", reason, res, r.lanes, r.err)
		}
	}
	r := newRig(poll5, rotated, copyOf("client", pairIn("acc-1", "dead-1", 20*time.Minute)),
		copyOf("realpath", pairIn("acc-2", "ref-2", 10*time.Minute)))
	if res := r.tick(primaryDir); !res.Granted || r.lanes != 0 || !slices.Equal(r.posted, []string{"dead-1", "ref-2"}) || !r.holds(rotated) {
		t.Fatalf("one live copy: want a grant after the dead client token, got %+v posted %v", res, r.posted)
	}
}

func TestUnreadableCopyIsErrorNotAbsence(t *testing.T) {
	bad := copyOf("realpath", Credential{})
	bad.Entry, _ = secrets.NewEntry([]byte(`{"Access":5}`))
	for _, want := range []error{errDenied, errBadEntry, nil} {
		r := newRig(poll5, rotated, copyOf("client", Credential{}), copyOf("realpath", pairIn("acc-1", "ref-1", 30*time.Minute)))
		if r.readErr = map[error]map[string]error{errDenied: {"realpath": errDenied}}[want]; want == errBadEntry {
			r.copies[1] = bad
		}
		if res := r.tick(primaryDir); want == nil && (r.err != nil || !res.Granted) {
			t.Fatalf("readable copies: want a grant, got %+v %v", res, r.err)
		} else if want != nil && (!errors.Is(r.err, want) || !strings.Contains(r.err.Error(), want.Error())) {
			t.Fatalf("want the injected error %q, got %v", want, r.err)
		} else if want != nil && (len(r.posted) != 0 || r.lanes != 0 || r.writes != 0 || res.AuthRequired || res.Reason == "no_refresh_token") {
			t.Fatalf("%v: a copy read error granted, wrote or went auth-required: %+v", want, res)
		}
	}
}

func TestInvalidGrantIsAuthRequired(t *testing.T) {
	outage := cascade.New(cascade.KindUnavailable, "test: token endpoint unreachable")
	for _, answer := range []any{nil, outage, rotated} {
		r := newRig(poll5, answer, oneCopy(30*time.Minute))
		res := r.tick(primaryDir)
		if answer == nil && (r.err != nil || !res.AuthRequired || res.Reason != "invalid_grant" || r.lanes != 1 || r.writes != 0) {
			t.Fatalf("invalid_grant: want auth-required with the copy unchanged, got %+v %v", res, r.err)
		}
		if answer == outage && (!errors.Is(r.err, outage) || !strings.Contains(r.err.Error(), outage.Error()) || r.lanes != 0 || res.AuthRequired) {
			t.Fatalf("outage: want the error and no lane write, got %+v %v", res, r.err)
		}
		if _, isPair := answer.(Credential); isPair && (r.err != nil || !res.Granted || r.lanes != 0) {
			t.Fatalf("live token: want a grant, got %+v %v", res, r.err)
		}
	}
}

func TestGrantGatedOnIdleCensus(t *testing.T) {
	for _, idle := range []time.Duration{-1, 5 * time.Minute, 20 * time.Minute} { // -1: a live session
		r, grant := newRig(poll5, rotated, oneCopy(30*time.Minute)), idle == 20*time.Minute
		if r.live = idle < 0; !r.live {
			r.idle = idle
		}
		if res := r.tick(primaryDir); r.err != nil || res.Granted != grant || res.Deferred == grant || len(r.posted) != posts(grant) {
			t.Fatalf("live=%v idle=%s: want grant=%v, got %+v posted %v", r.live, r.idle, grant, res, r.posted)
		}
	}
}

func TestGrantAfterExpiryDespiteLiveSession(t *testing.T) {
	for ago, grant := range map[time.Duration]bool{9 * time.Minute: false, 11 * time.Minute: true} {
		r := newRig(poll5, rotated, oneCopy(-ago))
		if r.live = true; r.tick(primaryDir).Granted != grant || r.err != nil || r.ran != 0 || len(r.posted) != posts(grant) {
			t.Fatalf("expired %s ago with a live session: want grant=%v, posted %v err %v", ago, grant, r.posted, r.err)
		}
	}
}

// refresherBinary is an executable candidate (a failed write fails its users).
func refresherBinary(t *testing.T) string {
	bin := filepath.Join(t.TempDir(), "harness")
	_ = os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o700)
	return bin
}

func TestAuthRequiredOnlyOnProviderEvidence(t *testing.T) {
	incomplete := Credential{Access: []byte("acc-2"), Expiry: grantT0.Add(time.Hour)}
	for _, answer := range []any{errors.New("test: connection reset"), incomplete, nil} {
		r := newRig(poll5, answer, oneCopy(30*time.Minute))
		r.refreshers = []string{refresherBinary(t)}
		res := r.tick(primaryDir)
		if evidence := answer == nil; evidence != (r.err == nil) || res.AuthRequired != evidence || r.lanes != posts(evidence) || r.ran != 0 {
			t.Fatalf("answer %T: want auth-required=%v and no PRIMARY CLI refresh, got %+v err %v runs %d", answer, evidence, res, r.err, r.ran)
		}
	}
}

func TestRefresherResolvedFromCandidates(t *testing.T) {
	bin := refresherBinary(t)
	r := newRig(poll5, nil, oneCopy(30*time.Minute))
	r.refreshers = []string{"harness", filepath.Join(t.TempDir(), "absent"), filepath.Dir(bin), bin}
	if res := r.tick(fleetDir); r.err != nil || r.ran != 1 || !res.AuthRequired || res.Reason != "invalid_grant" {
		t.Fatalf("stale CLI refresh: want one run then auth-required, got %+v runs %d err %v", res, r.ran, r.err)
	}
	if res := r.tick(fleetDir); r.ran != 1 || !res.AuthRequired {
		t.Fatalf("same expiry: want no second CLI refresh, got %+v runs %d", res, r.ran)
	}
	r = newRig(poll5, nil, oneCopy(30*time.Minute))
	r.refreshers, r.onRun = []string{bin}, func() { r.copies[0] = copyOf("client", pairIn("acc-9", "ref-9", 9*time.Hour)) }
	if res := r.tick(fleetDir); r.err != nil || r.ran != 1 || !res.Granted || res.Reason != "cli_refreshed" || r.lanes != 0 {
		t.Fatalf("fresh CLI refresh: want cli_refreshed, got %+v err %v", res, r.err)
	}
	r = newRig(poll5, nil, oneCopy(30*time.Minute))
	r.refreshers = []string{"harness", filepath.Dir(bin)}
	if res := r.tick(fleetDir); r.err != nil || r.ran != 0 || res.Reason != "refresher_binary_absent" || r.lanes != 1 {
		t.Fatalf("no candidate: want refresher_binary_absent, got %+v err %v", res, r.err)
	}
}

func TestAuthRefreshEventOkRotated(t *testing.T) {
	for refresh, isRotated := range map[string]bool{"ref-2": true, "ref-1": false} {
		r := newRig(poll5, pairIn("acc-2", refresh, 8*time.Hour), oneCopy(30*time.Minute))
		res := r.tick(primaryDir)
		want := AuthRefreshEvent{Kind: "auth.refresh", Account: "acct", OK: true, Rotated: isRotated}
		if r.err != nil || !slices.Equal(r.events, []AuthRefreshEvent{want}) || res.Rotated != isRotated {
			t.Fatalf("want %+v, got %+v err %v", want, r.events, r.err)
		}
		if out := fmt.Sprintf("%+v %#v %v", res, r.events, pairIn("acc-2", refresh, 0)); strings.Contains(out, "acc-") || strings.Contains(out, "ref-") {
			t.Fatalf("token bytes formatted: %s", out)
		}
	}
}

func TestRefreshLeadIsMaxOfHourAndTwoPolls(t *testing.T) {
	for _, tc := range []struct {
		poll, left time.Duration
		live, want bool
	}{{poll5, 59 * time.Minute, false, true}, {poll5, 2 * time.Hour, false, false},
		{90 * time.Minute, 2 * time.Hour, false, true}, {poll5, 59 * time.Minute, true, false}} {
		r := newRig(tc.poll, rotated, oneCopy(tc.left))
		if r.live = tc.live; r.tick(primaryDir).Granted != tc.want || r.err != nil || len(r.posted) != posts(tc.want) {
			t.Fatalf("poll %s, %s left, live %v: want grant=%v, posted %v err %v", tc.poll, tc.left, tc.live, tc.want, r.posted, r.err)
		}
		if tc.want && !r.holds(rotated) {
			t.Fatal("the granted pair did not reach every copy")
		}
	}
}
