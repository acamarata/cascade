//go:build !windows && integration

package main

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/client"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

type evidenceDaemon struct {
	w      *daemonWiring
	s      *jobs.Store
	http   *http.Client
	client *client.Client
}

func evidenceDaemonFixture(t *testing.T) evidenceDaemon {
	t.Helper()
	home := t.TempDir()
	for _, key := range []string{"HOME", "USERPROFILE", "CASCADE_HOME"} {
		t.Setenv(key, home)
	}
	seen := observeWiring(t)
	deps := daemonDeps{Paths: fakeDaemonPaths{root: home}, Getenv: func(string) string { return "" }, Environ: func() []string { return nil }, Clock: runtime.NewFixedClock(time.Unix(1700000000, 0))}
	opts, cleanups, err := composeDaemon(context.Background(), deps, nil)
	t.Cleanup(func() { runCleanupsLIFO(cleanups) })
	if err != nil {
		t.Fatal(err)
	}
	w := <-seen
	socket := filepath.Join(home, "s")
	l, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	w.Manifest.GoSupervised(w.Ctx, "evidence-test-http", "unix socket", func(context.Context) error {
		err := opts.Server.Serve(l)
		if err == http.ErrServerClosed {
			return nil
		}
		return err
	})
	t.Cleanup(func() { _ = opts.Server.Close() })
	dial := func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}
	transport := &http.Transport{DialContext: dial}
	t.Cleanup(transport.CloseIdleConnections)
	db, err := sql.Open("sqlite", filepath.Join(home, "data", "cascade.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	clientDial := func(ctx context.Context, path string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "unix", path)
	}
	return evidenceDaemon{w, jobs.NewStore(db), &http.Client{Transport: transport, Timeout: 5 * time.Second}, client.New(socket, clientDial, 5*time.Second)}
}

func (d evidenceDaemon) append(t *testing.T, ticket, key string, class provider.SensitivityTier) {
	t.Helper()
	ctx := context.Background()
	job := jobs.Job{ID: ticket, State: jobs.JobStatePending, ConsequenceClass: jobs.ConsequenceTrivial, DataClass: new(provider.SensitivityPublic)}
	if err := d.s.PutJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	if err := d.s.PutExecution(ctx, jobs.Execution{ID: "exec-" + ticket, JobID: ticket, State: jobs.ExecutionRunning}); err != nil {
		t.Fatal(err)
	}
	ls, err := d.w.Deps.Fleet.Jobs().LeasesForHolder(ctx, ticket)
	if err != nil {
		t.Fatal(err)
	}
	if len(ls) == 0 {
		grant, err := d.w.Deps.Fleet.Leases().Acquire(ctx, ticket+"-repo", "docs/**", ticket)
		if err != nil || !grant.Granted {
			t.Fatalf("lease: %+v %v", grant, err)
		}
		ls = []jobs.ResourceLease{grant.Lease}
	}
	_, err = d.w.Deps.Evidence.Append(ctx, jobs.EvidenceRecord{JobID: ticket, Kind: jobs.EvidenceBuild, ProducerCapability: jobs.ProducerControllerRun, AttestorIdentity: "daemon:test", Outcome: jobs.OutcomePass, IdempotencyKey: key, ArtifactRef: "artifact", ResultHash: "result", DataClass: &class}, jobs.AppendAuthorization{ExecutionID: "exec-" + ticket, LeaseRepoID: ls[0].RepoID, LeaseScopeGlob: ls[0].ScopeGlob, LeaseEpoch: ls[0].Epoch})
	if err != nil {
		t.Fatal(err)
	}
}

func (d evidenceDaemon) get(t *testing.T, query, marker string) *http.Response {
	t.Helper()
	r, err := http.NewRequestWithContext(context.Background(), "GET", "http://localhost/events?topic=jobs.evidence"+query, nil)
	if err != nil {
		t.Fatal(err)
	}
	if marker != "" {
		r.Header.Set(principal.HeaderOwnerCLI, marker)
	}
	res, err := d.http.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = res.Body.Close() })
	return res
}

func TestJobsEvidenceListMounted(t *testing.T) {
	d := evidenceDaemonFixture(t)
	d.append(t, "A", "one", provider.SensitivityInternal)
	d.append(t, "A", "two", provider.SensitivityPublic)
	for _, method := range []string{"jobs.evidence.list", "plugin.pbd.evidence"} {
		var reply struct {
			Ticket string        `json:"ticket"`
			Rows   []EvidenceRow `json:"rows"`
		}
		if err := d.client.Do(context.Background(), method, map[string]string{"ticket": "A"}, &reply); err != nil {
			t.Fatal(err)
		}
		if reply.Ticket != "A" || len(reply.Rows) != 2 || reply.Rows[0].Seq != 1 || reply.Rows[1].Seq != 2 || reply.Rows[0].DataClass != "internal" {
			t.Fatalf("reply %+v", reply)
		}
		if err := d.client.Do(context.Background(), method, map[string]string{"ticket": "unknown"}, &reply); err != nil || reply.Rows == nil || len(reply.Rows) != 0 {
			t.Fatalf("empty %+v, %v", reply, err)
		}
		if err := d.client.Do(context.Background(), method, map[string]string{"ticket": ""}, &reply); !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Fatalf("invalid: %v", err)
		}
	}
}

func TestJobsEvidenceAccessOwnerOnly(t *testing.T) {
	d := evidenceDaemonFixture(t)
	for _, method := range []string{"jobs.evidence.list", "plugin.pbd.evidence"} {
		var reply any
		if err := d.client.Do(context.Background(), method, map[string]string{"ticket": "A"}, &reply); err != nil {
			t.Fatal(err)
		}
	}
	res := d.get(t, "", "owner-cli")
	if res.StatusCode != 400 {
		t.Fatalf("owner status %d", res.StatusCode)
	}
	res = d.get(t, "&ticket=A", "")
	if res.StatusCode != 403 {
		t.Fatalf("anonymous status %d", res.StatusCode)
	}
	res = d.get(t, "&ticket=A", "wrong")
	if res.StatusCode != 403 {
		t.Fatalf("invalid marker status %d", res.StatusCode)
	}
	testEvidencePrincipalRefusal(t, d)
}

func testEvidencePrincipalRefusal(t *testing.T, d evidenceDaemon) {
	t.Helper()
	d.w.Registry.SetAccessPolicy(func(string) (principal.Principal, error) {
		return principal.Principal{ID: "test-session", Kind: principal.KindSession, SessionID: "s", Scope: "project:test"}, nil
	})
	for _, method := range []string{"jobs.evidence.list", "plugin.pbd.evidence"} {
		body := `{"jsonrpc":"2.0","id":1,"method":"` + method + `","params":{"ticket":"A"}}`
		r, err := http.NewRequestWithContext(context.Background(), "POST", "http://localhost/rpc", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set(principal.HeaderToken, "test-session-token")
		res, err := d.http.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := io.ReadAll(res.Body)
		_ = res.Body.Close()
		if err != nil || !strings.Contains(string(raw), cascade.KindPermissionDenied.String()) {
			t.Fatalf("principal response: %s, %v", raw, err)
		}
	}
	r, err := http.NewRequestWithContext(context.Background(), "GET", "http://localhost/events?topic=jobs.evidence&ticket=A", nil)
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set(principal.HeaderToken, "test-session-token")
	res, err := d.http.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != 403 {
		t.Fatalf("principal topic status: %d", res.StatusCode)
	}
}

func TestJobsEvidenceListRedactsOverClass(t *testing.T) {
	d := evidenceDaemonFixture(t)
	d.append(t, "A", "secret", provider.SensitivityLocalOnly)
	d.append(t, "A", "public", provider.SensitivityPublic)
	var reply struct {
		Rows []map[string]any `json:"rows"`
	}
	if err := d.client.Do(context.Background(), "jobs.evidence.list", map[string]string{"ticket": "A"}, &reply); err != nil {
		t.Fatal(err)
	}
	if len(reply.Rows) != 2 || reply.Rows[0]["redacted"] != true || reply.Rows[1]["redacted"] != false {
		t.Fatalf("reply %+v", reply)
	}
	for _, key := range []string{"artifact_ref", "result_hash", "attestor_identity", "idempotency_key"} {
		if _, ok := reply.Rows[0][key]; ok {
			t.Fatalf("leaked %s", key)
		}
		if _, ok := reply.Rows[1][key]; !ok {
			t.Fatalf("missing public %s", key)
		}
	}
}

func evidenceStreamCase(t *testing.T, tier provider.SensitivityTier) {
	t.Helper()
	d := evidenceDaemonFixture(t)
	d.append(t, "A", "history", provider.SensitivityPublic)
	res := d.get(t, "&ticket=A", "owner-cli")
	if res.StatusCode != 200 {
		t.Fatalf("subscribe status %d", res.StatusCode)
	}
	d.append(t, "B", "other", provider.SensitivityPublic)
	d.append(t, "A", "live", tier)
	scanner := bufio.NewScanner(res.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var got struct {
			jobs.EvidenceChanged
			Redacted bool `json:"redacted"`
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &got); err != nil {
			t.Fatal(err)
		}
		if got.JobID != "A" || got.Seq != 2 || got.Redacted != tier.MoreRestrictiveThan(jobs.EvidenceReadCeiling) {
			t.Fatalf("event %+v", got)
		}
		if strings.Contains(line, "artifact_ref") {
			t.Fatal("artifact exposed")
		}
		return
	}
	t.Fatalf("no event: %v", scanner.Err())
}

func TestJobsEvidenceTopicMounted(t *testing.T) { evidenceStreamCase(t, provider.SensitivityPublic) }
func TestJobsEvidenceSSERedactsOverClass(t *testing.T) {
	evidenceStreamCase(t, provider.SensitivityRestricted)
}
func TestDaemonEvidenceLedgerHasBus(t *testing.T) {
	evidenceStreamCase(t, provider.SensitivityInternal)
}

func TestJobsEvidenceTopicRequiresTicket(t *testing.T) {
	d := evidenceDaemonFixture(t)
	for _, query := range []string{"", "&ticket=", "&ticket=A&ticket=B"} {
		res := d.get(t, query, "owner-cli")
		raw, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		if res.StatusCode != 400 || !strings.Contains(string(raw), cascade.KindInvalidInput.String()) {
			t.Fatalf("%s: %d %s", query, res.StatusCode, raw)
		}
	}
	res := d.get(t, "&ticket=A", "owner-cli")
	if res.StatusCode != 200 {
		t.Fatalf("valid subscription: %d", res.StatusCode)
	}
}
