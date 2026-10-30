//go:build integration

// Purpose (this file): P1-CPA-14 acceptance [4], the transport live leg.
//   getMe through the production Doer's own transport, host pin, URL builder
//   and envelope decoder (newAPIDoer over the production HTTP poster) behind
//   the real EgressClassBridge gate. The Doer's three-method allowlist
//   refuses getMe (asserted first, so a change there is seen); the leg
//   therefore posts through doer.transport at doer.methodURL, which is Do
//   minus the allowlist. It asserts ok and the
//   bot's own identity, sends nothing, addresses no chat and reads no
//   channel variable. Unset, it skips naming the variable (CI); the build
//   session runs it once with the token exported from the vault.
//
// Constraints: no bot id, username or token in any log or failure message.
//
// SPORT: plugins/cascade-pa/telegram TestTelegramTransportLiveSend/TEST
//   (P1-CPA-14).

package telegram

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
	cascadepa "github.com/acamarata/cascade/plugins/cascade-pa"
)

// transportTokenVar is the one variable this leg reads.
const transportTokenVar = "CASCADE_ACCEPT_TELEGRAM_BOT_TOKEN"

// methodGetMe is the Bot API identity call; the production client never
// needs it, so it is named here.
const methodGetMe = "getMe"

func TestTelegramTransportLiveSend(t *testing.T) {
	token := os.Getenv(transportTokenVar)
	if token == "" {
		t.Skipf("transport live leg: %s is unset", transportTokenVar)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, engine := acceptRealFirewall(t, t.TempDir(), "", "")
	gate := realAcceptEgressGate{engine: engine}
	if _, err := gate.Guard(ctx, cascadepa.TierInternal, nil); err != nil {
		t.Fatalf("the bridge egress gate refused the getMe probe: kind %v", errKind(err))
	}
	doer := newAPIDoer(token, newHTTPPoster(nil))
	if err := doer.Do(ctx, methodGetMe, struct{}{}, &User{}); !cascade.HasKind(err, cascade.KindUnsupported) {
		t.Fatalf("the production Doer's method allowlist admitted getMe (kind %v); this leg's premise changed", errKind(err))
	}
	recorder := &liveURLRecorder{inner: doer.transport}
	raw, err := recorder.Post(ctx, doer.methodURL(methodGetMe), []byte("{}"))
	if err != nil {
		t.Fatalf("getMe did not reach the Bot API: kind %v", errKind(postFailed(methodGetMe)))
	}
	var me User
	if err := decodeEnvelope(raw, &me); err != nil {
		t.Fatalf("getMe answered ok=false or an undecodable envelope: kind %v", errKind(err))
	}
	assertOwnIdentity(t, token, me)
	if len(recorder.urls) != 1 || !strings.HasSuffix(recorder.urls[0], "/"+methodGetMe) ||
		!strings.HasPrefix(recorder.urls[0], telegramAPIBase+"/bot") {
		t.Fatal("the leg posted something other than one getMe to the pinned Bot API host")
	}
}

// assertOwnIdentity requires a bot user whose id is the token's own id
// prefix and which has a username, without echoing either value.
func assertOwnIdentity(t *testing.T, token string, me User) {
	t.Helper()
	prefix, _, ok := strings.Cut(token, ":")
	if !ok {
		t.Fatal("the token has no id prefix")
	}
	want, err := strconv.ParseInt(prefix, 10, 64)
	if err != nil {
		t.Fatal("the token's id prefix is not numeric")
	}
	if !me.IsBot {
		t.Fatal("getMe answered a user that is not a bot")
	}
	if me.ID != want {
		t.Fatal("getMe answered an id other than the token's own bot id")
	}
	if me.Username == "" {
		t.Fatal("getMe answered no username")
	}
}

// liveURLRecorder records the URL of every post (never logged: it carries
// the token), so the leg proves it posted getMe and nothing else.
type liveURLRecorder struct {
	inner poster
	urls  []string
}

func (r *liveURLRecorder) Post(ctx context.Context, url string, body []byte) ([]byte, error) {
	r.urls = append(r.urls, url)
	return r.inner.Post(ctx, url, body)
}

// errKind reports only an error's Kind: a transport error may echo the
// request URL, which carries the token.
func errKind(err error) cascade.Kind {
	k, _ := cascade.KindOf(err)
	return k
}
