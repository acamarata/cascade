package executive

import (
	"bytes"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/ci"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/rpc"
)

const adapterGoldenPath = "testdata/adapter-table.golden.json"

func isolateAdapterHome(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CASCADE_HOME", t.TempDir())
}

func bevent(kind events.EventKind, payload string) events.Event {
	return events.Event{Kind: kind, Payload: []byte(payload)}
}

func TestEXE01LegacyAC8(t *testing.T) {
	isolateAdapterHome(t)
	var logs bytes.Buffer
	a := NewAdapter(slog.New(slog.NewTextHandler(&logs, nil)))
	checkAdapterMapsAndSkips(t, a)
	if a.Dropped() != 0 || logs.Len() != 0 {
		t.Fatalf("after mapped and skipped: Dropped %d, logs %q", a.Dropped(), logs.String())
	}
	dropped := []struct {
		ns string
		ev events.Event
	}{
		{"jobs", bevent("job.unknown", `{}`)},
		{"jobs", bevent("job.transitioned", `{"to":"pending","secret_marker":"zz"}`)},
		{"ci_results", bevent("ci.run.completed", `{"passed":"true"}`)},
		{"jobs", bevent("job.transitioned", `{"to":`)},
		{"policy", bevent("policy.decision", `{}`)},
	}
	for _, c := range dropped {
		if got, out := a.Map(c.ns, c.ev); got != "" || out != OutcomeDropped {
			t.Errorf("Map(%s, %s %s) = %q %s, want dropped", c.ns, c.ev.Kind, c.ev.Payload, got, out)
		}
	}
	if a.Dropped() != 5 {
		t.Fatalf("Dropped = %d, want 5", a.Dropped())
	}
	lines := strings.Split(strings.TrimSpace(logs.String()), "\n")
	if len(lines) != 5 {
		t.Fatalf("WARN lines = %d, want 5: %q", len(lines), logs.String())
	}
	for i, line := range lines {
		if !strings.Contains(line, "level=WARN") || !strings.Contains(line, "kind="+string(dropped[i].ev.Kind)) {
			t.Errorf("line %d = %q, want WARN naming %s", i, line, dropped[i].ev.Kind)
		}
		if strings.Contains(line, "secret_marker") {
			t.Errorf("line %d logs the payload: %q", i, line)
		}
	}
	if NewAdapter(nil).logger != slog.Default() {
		t.Fatal("NewAdapter(nil) does not use slog.Default()")
	}
}

// checkAdapterMapsAndSkips: each known bus event maps to its exec kind, and
// the accepted transition and the lease grant are skipped.
func checkAdapterMapsAndSkips(t *testing.T, a *Adapter) {
	t.Helper()
	mapped := []struct {
		ns   string
		ev   events.Event
		want Kind
	}{
		{"jobs", bevent("job.transitioned", `{"job_id":"j1","to":"running"}`), KindTaskStarted},
		{"jobs", bevent("job.transitioned", `{"to": "verifying"}`), KindVerifyRequested},
		{"jobs", bevent("job.failed", `{}`), KindTaskFailed},
		{"ci_results", bevent("ci.run.completed", `{"run_id":1,"passed":false}`), KindVerifyFailed},
		{"ci_results", bevent("ci.run.completed", `{"passed":true}`), KindVerifyPassed},
		{"jobs.lease", bevent("jobs.lease.contended", `{}`), KindLeaseConflict},
	}
	for _, c := range mapped {
		if got, out := a.Map(c.ns, c.ev); got != c.want || out != OutcomeMapped {
			t.Errorf("Map(%s, %s) = %q %s, want %q mapped", c.ns, c.ev.Kind, got, out, c.want)
		}
	}
	for _, ev := range []events.Event{bevent("job.transitioned", `{"to":"accepted"}`), bevent("job.leased", `{}`)} {
		if got, out := a.Map("jobs", ev); got != "" || out != OutcomeSkipped {
			t.Errorf("Map(jobs, %s) = %q %s, want skipped", ev.Payload, got, out)
		}
	}
}

type adapterGolden struct {
	Namespaces []string      `json:"namespaces"`
	Rows       []AdapterRow  `json:"rows"`
	Producers  []ProducerRow `json:"producers"`
}

func marshalAdapterTables(t *testing.T) []byte {
	t.Helper()
	b, err := json.MarshalIndent(adapterGolden{SourceNamespaces(), AdapterRows(), ProducerRows()}, "", "  ")
	if err != nil {
		t.Fatalf("marshal tables: %v", err)
	}
	return append(b, '\n')
}

func TestEXE01LegacyAC9(t *testing.T) {
	isolateAdapterHome(t)
	nsSet := make(map[string]bool)
	for _, ns := range SourceNamespaces() {
		nsSet[ns] = true
	}
	seen := make(map[string]bool)
	covered := make(map[Kind]int)
	for _, r := range AdapterRows() {
		if !nsSet[r.Namespace] {
			t.Errorf("row namespace %q not in SourceNamespaces", r.Namespace)
		}
		key := r.Namespace + "\x00" + string(r.BusKind) + "\x00" + r.Field + "\x00" + string(r.Value)
		if seen[key] {
			t.Errorf("duplicate row %q", key)
		}
		seen[key] = true
		if r.Exec != "" {
			covered[r.Exec] |= 1
		}
	}
	for _, p := range ProducerRows() {
		if covered[p.Exec]&1 != 0 {
			t.Errorf("%s has both a mapped row and a producer row", p.Exec)
		}
		covered[p.Exec] |= 2
	}
	if len(covered) != len(AllKinds) || len(AllKinds) != 41 {
		t.Fatalf("covered %d kinds, AllKinds %d, want 41", len(covered), len(AllKinds))
	}
	for _, k := range AllKinds {
		if covered[k] == 0 {
			t.Errorf("%s has no row", k)
		}
	}
	got := marshalAdapterTables(t)
	if os.Getenv("UPDATE_ADAPTER_GOLDEN") != "" {
		if err := os.WriteFile(adapterGoldenPath, got, 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
	}
	want, err := os.ReadFile(filepath.FromSlash(adapterGoldenPath))
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("golden differs from the tables:\n%s", got)
	}
}

func TestEXE01LegacyAC10(t *testing.T) {
	isolateAdapterHome(t)
	f, err := parser.ParseFile(token.NewFileSet(), "events.go", nil, 0)
	if err != nil {
		t.Fatalf("parse events.go: %v", err)
	}
	for _, imp := range f.Imports {
		path, _ := strconv.Unquote(imp.Path.Value)
		first := strings.SplitN(path, "/", 2)[0]
		if path != "github.com/acamarata/cascade/internal/events" && strings.Contains(first, ".") {
			t.Errorf("events.go imports %q", path)
		}
	}
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if id, ok := sel.X.(*ast.Ident); ok && id.Name == "events" && sel.Sel.Name == "New" {
			t.Errorf("events.go calls events.New")
		}
		if strings.HasPrefix(sel.Sel.Name, "Publish") || strings.HasPrefix(sel.Sel.Name, "Subscribe") {
			t.Errorf("events.go calls %s", sel.Sel.Name)
		}
		return true
	})
}

func TestAdapterDormantRowsListed(t *testing.T) {
	isolateAdapterHome(t)
	var want []Kind
	for _, k := range AllKinds {
		for _, p := range ProducerRows() {
			if p.Exec == k && !p.Live {
				want = append(want, k)
			}
		}
		if k == KindDecisionRequired {
			want = append(want, k)
		}
	}
	got := DormantKinds()
	if len(got) != 29 || !reflect.DeepEqual(got, want) {
		t.Fatalf("DormantKinds = %v (%d), want %v", got, len(got), want)
	}
	for _, k := range got {
		if k == KindT0Awake {
			t.Fatal("T0_AWAKE is dormant")
		}
	}
	got[0] = "mutated"
	if DormantKinds()[0] == "mutated" {
		t.Fatal("DormantKinds is not a fresh copy")
	}
}

func TestAdapterBusKindsMatchProducers(t *testing.T) {
	isolateAdapterHome(t)
	wantKinds := map[events.EventKind]bool{
		rpc.EventJobLeased: true, rpc.EventJobTransitioned: true,
		rpc.EventJobCompleted: true, rpc.EventJobFailed: true,
		jobs.EventLeaseAcquired: true, jobs.EventLeaseContended: true, jobs.EventLeaseRenewed: true,
		jobs.EventLeaseReleased: true, jobs.EventLeaseExpired: true,
		ci.EventKindRunCompleted: true, busNotification: true,
	}
	wantNS := map[string]bool{jobs.TransitionNamespace: true, "jobs.lease": true, ci.EventNamespace: true, nsNotify: true}
	gotKinds := make(map[events.EventKind]bool)
	gotNS := make(map[string]bool)
	for _, r := range AdapterRows() {
		gotKinds[r.BusKind] = true
		gotNS[r.Namespace] = true
	}
	if !reflect.DeepEqual(gotKinds, wantKinds) {
		t.Errorf("row kinds = %v, want %v", gotKinds, wantKinds)
	}
	if !reflect.DeepEqual(gotNS, wantNS) {
		t.Errorf("row namespaces = %v, want %v", gotNS, wantNS)
	}
	for _, r := range AdapterRows() {
		if strings.HasPrefix(string(r.BusKind), "jobs.lease.") && r.Namespace != "jobs.lease" {
			t.Errorf("lease kind %s on namespace %s", r.BusKind, r.Namespace)
		}
	}
}
