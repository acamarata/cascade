package sync

// Purpose (this file): the two-peer sync acceptance drill's harness core —
//   how the enrolled second machine is resolved, how one round trip runs,
//   and the dress-rehearsal lane that proves the script itself.
//
// THE HARNESS FAILS CLOSED AND NEVER SUBSTITUTES. A drill whose target is
//   unset is a typed error, not a loopback run wearing the drill's name.
//   The distinction matters because the whole value of an acceptance
//   ticket is that it ran against a real second machine: a harness that
//   quietly fell back to two engines in one process would report the
//   acceptance as passed on evidence nobody asked for.
//
// WHAT RUNS WHERE. The rehearsal (TestSyncRoundTripDressRehearsalLoopback,
//   acceptance_conflicts_test.go) runs unconditionally and proves the
//   SCRIPT. The real drill (acceptance_evidence_test.go) runs only with a
//   target configured, and is the 06 §7 owner prerequisite. The only
//   difference between them is the peer.
//
// SPORT: sync/acceptance-drill (ADD) — P1-E17-W4-S38-T5.

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/internal/storage"
	"github.com/acamarata/cascade/pkg/cascade"
)

// The environment the drill's target comes from (08 §2's CASCADE_* map).
// Read from the environment rather than a flag so a non-interactive run
// can supply it, and so CASCADE_NO_INPUT=1 changes nothing here — there is
// nothing to prompt for.
const (
	// EnvAcceptanceTarget is the enrolled second machine, user@host[:port].
	envAcceptanceTarget = "CASCADE_ACCEPTANCE_SYNC_TARGET"
	// envAcceptanceNodeID is that machine's enrolled node id.
	envAcceptanceNodeID = "CASCADE_ACCEPTANCE_SYNC_NODE_ID"
)

// acceptanceTarget is the enrolled second machine the real drill runs
// against.
type acceptanceTarget struct {
	NodeID string
	User   string
	Addr   string
}

// resolveAcceptanceTarget reads the target from getenv.
//
// Every failure is a TYPED error naming what to set. It is never a skip
// and never a loopback substitution: see this file's header for why that
// is the whole point of the harness rather than a detail of it.
func resolveAcceptanceTarget(getenv func(string) string) (acceptanceTarget, error) {
	if getenv == nil {
		return acceptanceTarget{}, cascade.New(cascade.KindInternal,
			"sync acceptance: the drill was built with no environment to read its target from")
	}
	raw := strings.TrimSpace(getenv(envAcceptanceTarget))
	if raw == "" {
		return acceptanceTarget{}, cascade.Newf(cascade.KindUnavailable,
			"sync acceptance: no second machine is configured; set %s=user@host[:port] and %s=<node id>. "+
				"The loopback rehearsal proves the script and is not this drill",
			envAcceptanceTarget, envAcceptanceNodeID)
	}
	at := strings.IndexByte(raw, '@')
	if at <= 0 || at == len(raw)-1 {
		return acceptanceTarget{}, cascade.Newf(cascade.KindInvalidInput,
			"sync acceptance: %s=%q is not user@host[:port]", envAcceptanceTarget, raw)
	}
	user, host := raw[:at], withDefaultSSHPort(raw[at+1:])
	nodeID := strings.TrimSpace(getenv(envAcceptanceNodeID))
	if nodeID == "" {
		return acceptanceTarget{}, cascade.Newf(cascade.KindInvalidInput,
			"sync acceptance: %s is set but %s is not; the drill asserts trust-tier eligibility "+
				"for a specific enrolled node and cannot infer which one",
			envAcceptanceTarget, envAcceptanceNodeID)
	}
	return acceptanceTarget{NodeID: nodeID, User: user, Addr: host}, nil
}

// withDefaultSSHPort appends the default ssh port when host carries none.
//
// Written with strings rather than net.SplitHostPort DELIBERATELY: this
// file is in the unconditional unit lane, and that lane's gate refuses an
// import of net outright (a test that can dial is a test that will, one
// edit later). Splitting a host from a port is string work, and the only
// case that needs care is an IPv6 literal, which is bracketed precisely so
// its colons can be told from a port separator.
func withDefaultSSHPort(host string) string {
	const defaultPort = ":22"
	if strings.HasSuffix(host, "]") {
		// A bracketed IPv6 literal with no port.
		return host + defaultPort
	}
	if strings.Contains(host, ":") {
		// Either host:port, or an unbracketed IPv6 literal — which is not
		// a form this accepts anyway, and is left alone rather than
		// mangled into something that looks valid.
		return host
	}
	return host + defaultPort
}

// TestTheDefaultPortIsAppendedOnlyWhenThereIsNone covers the shapes a
// target can arrive in, including the bracketed IPv6 literal that is the
// whole reason this is not a naive colon search.
func TestTheDefaultPortIsAppendedOnlyWhenThereIsNone(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"server.example", "server.example:22"},
		{"server.example:2222", "server.example:2222"},
		{"[2001:db8::1]", "[2001:db8::1]:22"},
		{"[2001:db8::1]:2222", "[2001:db8::1]:2222"},
	} {
		if got := withDefaultSSHPort(tc.in); got != tc.want {
			t.Errorf("withDefaultSSHPort(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestSyncAcceptanceTargetRequired is the ticket's named check: the
// harness refuses an unset or unusable target, typed, every time.
//
// Each case asserts the KIND as well as the failure, because "unavailable"
// (nothing is configured) and "invalid input" (something is configured
// wrongly) are the two different things an operator does something
// different about.
func TestSyncAcceptanceTargetRequired(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  map[string]string
		want cascade.Kind
	}{
		{"nothing configured", nil, cascade.KindUnavailable},
		{"blank target", map[string]string{envAcceptanceTarget: "   "}, cascade.KindUnavailable},
		{"not user@host", map[string]string{
			envAcceptanceTarget: "justahost", envAcceptanceNodeID: "n1",
		}, cascade.KindInvalidInput},
		{"no user", map[string]string{
			envAcceptanceTarget: "@host", envAcceptanceNodeID: "n1",
		}, cascade.KindInvalidInput},
		{"no host", map[string]string{
			envAcceptanceTarget: "user@", envAcceptanceNodeID: "n1",
		}, cascade.KindInvalidInput},
		{"target without a node id", map[string]string{
			envAcceptanceTarget: "user@host",
		}, cascade.KindInvalidInput},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := resolveAcceptanceTarget(func(k string) string { return tc.env[k] })
			if err == nil {
				t.Fatal("the drill resolved a target it should have refused")
			}
			if kind, ok := cascade.KindOf(err); !ok || kind != tc.want {
				t.Errorf("kind = %v (ok=%v), want %v: %v", kind, ok, tc.want, err)
			}
			if strings.Contains(err.Error(), "loopback") && tc.want != cascade.KindUnavailable {
				t.Error("a malformed target was answered with the loopback note, which belongs " +
					"only on the nothing-is-configured case")
			}
		})
	}

	// A well-formed target resolves, with the default port filled in — the
	// other half of the assertion, without which every case above would
	// pass for a resolver that refused everything.
	got, err := resolveAcceptanceTarget(func(k string) string {
		return map[string]string{
			envAcceptanceTarget: "ops@server.example", envAcceptanceNodeID: "node-7",
		}[k]
	})
	if err != nil {
		t.Fatalf("a well-formed target was refused: %v", err)
	}
	if got.User != "ops" || got.Addr != "server.example:22" || got.NodeID != "node-7" {
		t.Errorf("resolved %+v, want ops@server.example:22 as node-7", got)
	}
}

// TestTheDrillNeverSubstitutesLoopback pins the rule the header states: no
// input resolves to a local address on its own. A harness that filled in a
// default target would run the rehearsal and call it the acceptance.
func TestTheDrillNeverSubstitutesLoopback(t *testing.T) {
	for _, env := range []map[string]string{nil, {envAcceptanceNodeID: "n1"}} {
		got, err := resolveAcceptanceTarget(func(k string) string { return env[k] })
		if err == nil {
			t.Fatalf("an unconfigured drill resolved %+v", got)
		}
		if got.Addr != "" {
			t.Errorf("a refused resolution still produced an address: %q", got.Addr)
		}
	}
}

// acceptancePeer is one side of the drill: an engine, its conflict
// journal, and the records it holds per domain.
type acceptancePeer struct {
	name   string
	engine *Engine
	state  map[string]map[string]Record
}

// newAcceptancePeer builds one side over its own store, so the two peers
// share nothing but the wire.
func newAcceptancePeer(t *testing.T, name string) *acceptancePeer {
	t.Helper()
	return &acceptancePeer{
		name:   name,
		engine: NewEngine(newFakeStore(), fakeClock{t: time.Now()}, newTestEgressEngine(t)),
		state:  map[string]map[string]Record{},
	}
}

// put seeds one record on this side.
func (p *acceptancePeer) put(rec Record) {
	key := string(rec.Domain) + "/" + rec.Subkind
	if p.state[key] == nil {
		p.state[key] = map[string]Record{}
	}
	p.state[key][rec.ID] = rec
}

// records returns this side's records for a domain, in send order.
func (p *acceptancePeer) records(domain storage.DomainID, subkind string) []Record {
	var out []Record
	for _, rec := range p.state[string(domain)+"/"+subkind] {
		out = append(out, rec)
	}
	return out
}

// wireCarrier moves an encoded batch from the sender to the receiver.
//
// It is a parameter rather than an assumption because it is the ONLY
// difference between the rehearsal and a run over a real transport: the
// rehearsal hands the bytes straight across, and the sshd lane ships them
// to a remote file over a real ssh session and reads them back. Threading
// it means the drill script is literally the same code in both, which is
// what "the rehearsal proves the script" has to mean to be worth saying.
type wireCarrier func(t *testing.T, frames []byte) []byte

// localCarrier is the rehearsal's transport: the bytes, unchanged.
func localCarrier(_ *testing.T, frames []byte) []byte { return frames }

// syncLeg runs one direction: from ships its records, carry moves the
// bytes, to receives and merges them under the domain's own strategy.
func syncLeg(
	t *testing.T, carry wireCarrier, from, to *acceptancePeer,
	domain storage.DomainID, subkind string, tier nodes.Tier,
) MergeResult {
	t.Helper()
	var wire bytes.Buffer
	recs := from.records(domain, subkind)
	if _, err := from.engine.SendBatch(context.Background(), &wire,
		domain, subkind, recs, 1, 0); err != nil {
		t.Fatalf("%s → %s: SendBatch(%s/%s): %v", from.name, to.name, domain, subkind, err)
	}
	arrived := bytes.NewReader(carry(t, wire.Bytes()))
	res, err := to.engine.ReceiveAndMerge(context.Background(), arrived, MergeRequest{
		Domain: domain, Subkind: subkind, PeerTier: tier,
		Local: to.state[string(domain)+"/"+subkind],
	}, 1, 0)
	if err != nil {
		t.Fatalf("%s → %s: ReceiveAndMerge(%s/%s): %v", from.name, to.name, domain, subkind, err)
	}
	to.state[string(domain)+"/"+subkind] = res.Records
	return res
}
