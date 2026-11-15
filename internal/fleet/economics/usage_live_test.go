//go:build live

package economics

// Purpose: the two live smokes the release hardening gate runs against a
// real subscription account: one usage GET with the account's freshest
// access token, and one RefreshGrantOnce through the real copy set with a
// real token exchange. They change the account's live credential (the grant
// rotates it), so they run only on an explicit, operator-owned run.
// Constraints: without CASCADE_LIVE_SUBSCRIPTION_SMOKE=1 both skip and name
// the flag; with it set, a missing setting FAILS. The endpoints come from
// the environment (no host in Go source). HTTP goes through curl with its
// config on stdin, so no token reaches argv, and this file imports no net.
// Token bytes are never logged or put in a failure message.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/providers/agents/claude"
)

const liveSmokeFlag = "CASCADE_LIVE_SUBSCRIPTION_SMOKE"

// liveSetting returns a required setting, failing the run when it is unset.
func liveSetting(t *testing.T, name string) string {
	t.Helper()
	v := os.Getenv(name)
	if v == "" {
		t.Fatalf("%s=1 needs %s", liveSmokeFlag, name)
	}
	return v
}

// liveSetup skips without the flag and returns the account, its endpoints
// and grant deps over the real harness credentials (custody: a temp vault).
func liveSetup(t *testing.T) (AccountRef, claude.UsageDescriptor, GrantDeps) {
	t.Helper()
	if os.Getenv(liveSmokeFlag) != "1" {
		t.Skipf("live smoke: set %s=1 (and the CASCADE_LIVE_* settings) to run it", liveSmokeFlag)
	}
	acct := AccountRef{ID: "live", ConfigDir: liveSetting(t, "CASCADE_LIVE_HARNESS_CONFIG_DIR")}
	desc := claude.UsageDescriptor{Host: liveSetting(t, "CASCADE_LIVE_USAGE_HOST"),
		UsagePath: liveSetting(t, "CASCADE_LIVE_USAGE_PATH"), RefreshPath: liveSetting(t, "CASCADE_LIVE_REFRESH_PATH")}
	custody, err := secrets.SelectCustody(secrets.Config{Service: "cascade-live-smoke", Dir: t.TempDir(), ForceFileVault: true, Runner: func(context.Context, string, ...string) ([]byte, error) {
		return nil, errors.New("live smoke: no keychain")
	}})
	if err != nil {
		t.Fatal(err)
	}
	creds, err := secrets.NewHarnessCredentials(custody, runtime.NewSystemClock())
	home, herr := os.UserHomeDir()
	if err != nil || herr != nil {
		t.Fatal(err, herr)
	}
	deps := GrantDeps{Creds: creds, Codec: liveCodec{}, Clock: runtime.NewSystemClock(), Memo: &GrantMemo{},
		DefaultDir: filepath.Join(home, ".claude"), UsagePoll: func() time.Duration { return 5 * time.Minute },
		Census:       func(context.Context, AccountRef) (bool, time.Duration, error) { return false, time.Hour, nil },
		AuthRequired: func(context.Context, AccountRef) error { return errors.New("live smoke: auth-required") },
		Publish:      func(context.Context, AuthRefreshEvent) error { return nil },
		RunRefresher: func(context.Context, string, string) error { return errors.New("live smoke: no CLI refresh") },
	}
	deps.Exchange = func(ctx context.Context, refresh []byte) (Credential, error) { return liveExchange(ctx, desc, refresh) }
	return acct, desc, deps
}

func TestClaudeUsageLiveSmoke(t *testing.T) {
	acct, desc, deps := liveSetup(t)
	ctx := context.Background()
	tk, err := readTick(ctx, deps, acct)
	if err != nil {
		t.Fatalf("reading the copy set: %v", err)
	}
	f := tk.freshest()
	if f < 0 {
		t.Fatal("no copy holds a full token pair")
	}
	doc, err := claude.FetchUsage(ctx, desc, tk.creds[f].Access, curlDoer{})
	if err != nil || len(doc.Windows) == 0 {
		t.Fatalf("usage GET: %d windows, %v", len(doc.Windows), err)
	}
	t.Logf("usage GET: %d windows", len(doc.Windows))
}

func TestClaudeRefreshGrantLiveSmoke(t *testing.T) {
	acct, _, deps := liveSetup(t)
	ctx := context.Background()
	before, err := readTick(ctx, deps, acct)
	if err != nil || before.freshest() < 0 {
		t.Fatalf("reading the copy set: freshest %d, %v", before.freshest(), err)
	}
	// Due now: the clock stands 30 minutes before the freshest expiry.
	deps.Clock = runtime.NewFixedClock(before.expiry().Add(-30 * time.Minute))
	res, err := RefreshGrantOnce(ctx, deps, acct)
	if err != nil || !res.Granted || res.Posts != 1 || res.AuthRequired {
		t.Fatalf("live grant: %+v, %v", res, err)
	}
	after, err := readTick(ctx, deps, acct)
	for i, c := range after.creds {
		if err != nil || len(c.Access) == 0 || len(c.Refresh) == 0 || !c.Expiry.After(before.expiry()) {
			t.Fatalf("copy %d does not hold the rotated pair (%v)", i, err)
		}
	}
}

// liveExchange posts the refresh grant and maps a refused token to
// secrets.ErrInvalidGrant; no response body reaches an error.
func liveExchange(ctx context.Context, desc claude.UsageDescriptor, refresh []byte) (Credential, error) {
	req, err := claude.RefreshRequest(desc, refresh, runtime.NewSystemClock().Now())
	if err != nil {
		return Credential{}, err
	}
	body, status, err := curlDoer{}.Do(ctx, req.Method, req.URL, req.Body, req.Headers)
	if err != nil {
		return Credential{}, err
	}
	var got struct {
		Access    string `json:"access_token"`
		Refresh   string `json:"refresh_token"`
		ExpiresIn int64  `json:"expires_in"`
		Error     string `json:"error"`
	}
	if jerr := json.Unmarshal(body, &got); jerr != nil || got.Error == "invalid_grant" || status != 200 {
		if got.Error == "invalid_grant" {
			return Credential{}, cascade.Wrap(cascade.KindPermissionDenied, secrets.ErrInvalidGrant, "live smoke: grant refused")
		}
		return Credential{}, cascade.Newf(cascade.KindUnavailable, "live smoke: grant answered HTTP %d", status)
	}
	return Credential{Access: []byte(got.Access), Refresh: []byte(got.Refresh),
		Expiry: runtime.NewSystemClock().Now().Add(time.Duration(got.ExpiresIn) * time.Second)}, nil
}

// liveCodec reads and writes the harness's own entry shape, keeping every
// field it does not own.
type liveCodec struct{}

type liveOAuth struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	ExpiresAt    int64  `json:"expiresAt"`
}

func (liveCodec) Decode(entry []byte) (Credential, error) {
	var doc struct {
		OAuth liveOAuth `json:"claudeAiOauth"`
	}
	if err := json.Unmarshal(entry, &doc); err != nil {
		return Credential{}, cascade.New(cascade.KindIntegrity, "live smoke: the entry is not the harness shape")
	}
	c := Credential{Access: []byte(doc.OAuth.AccessToken), Refresh: []byte(doc.OAuth.RefreshToken)}
	if doc.OAuth.ExpiresAt > 0 {
		c.Expiry = time.UnixMilli(doc.OAuth.ExpiresAt).UTC()
	}
	return c, nil
}

func (liveCodec) Encode(prior []byte, c Credential) ([]byte, error) {
	doc, inner := map[string]json.RawMessage{}, map[string]any{}
	if len(bytes.TrimSpace(prior)) > 0 && json.Unmarshal(prior, &doc) != nil {
		return nil, cascade.New(cascade.KindIntegrity, "live smoke: the prior entry is not a JSON object")
	}
	if raw, ok := doc["claudeAiOauth"]; ok && json.Unmarshal(raw, &inner) != nil {
		return nil, cascade.New(cascade.KindIntegrity, "live smoke: the prior oauth block is not a JSON object")
	}
	inner["accessToken"], inner["refreshToken"], inner["expiresAt"] = string(c.Access), string(c.Refresh), c.Expiry.UnixMilli()
	raw, err := json.Marshal(inner)
	if err != nil {
		return nil, err
	}
	doc["claudeAiOauth"] = raw
	return json.Marshal(doc)
}

// curlDoer runs one request through curl with its whole config (URL,
// headers, body) on stdin, so no token reaches argv or a log.
type curlDoer struct{}

func (curlDoer) Do(ctx context.Context, method, url string, body []byte, headers map[string]string) ([]byte, int, error) {
	var cfg strings.Builder
	fmt.Fprintf(&cfg, "url = %q\nrequest = %q\nsilent\nshow-error\nmax-time = 30\nwrite-out = \"\\n%%{http_code}\"\n", url, method)
	for k, v := range headers {
		fmt.Fprintf(&cfg, "header = %q\n", k+": "+v)
	}
	if body != nil {
		fmt.Fprintf(&cfg, "data-binary = %q\n", string(body))
	}
	cmd := exec.CommandContext(ctx, "curl", "-K", "-")
	cmd.Stdin = strings.NewReader(cfg.String())
	out, err := cmd.Output()
	if err != nil {
		return nil, 0, cascade.New(cascade.KindUnavailable, "live smoke: curl failed")
	}
	cut := bytes.LastIndexByte(out, '\n')
	status, err := strconv.Atoi(string(out[cut+1:]))
	if cut < 0 || err != nil {
		return nil, 0, cascade.New(cascade.KindUnavailable, "live smoke: curl wrote no status")
	}
	return out[:cut], status, nil
}
