// Purpose: tests for the secret values BuildDriverEnv returns for the output
// scrub when the per-spawn proxy URLs carry different or escaped credentials.
// SPORT: internal.providers.agents tests (ADD) - P1-AGT-15.

package agents

import (
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// TestBuildDriverEnvMasksEveryProxyCredential: a credential that differs per
// proxy variable is listed for the scrub, not only the one in HTTPS_PROXY.
func TestBuildDriverEnvMasksEveryProxyCredential(t *testing.T) {
	proxy := []string{"NO_PROXY=", "no_proxy="}
	for _, name := range []string{"HTTPS_PROXY", "HTTP_PROXY", "https_proxy", "http_proxy"} {
		proxy = append(proxy, name+"=http://cascade:pw-"+name+"@127.0.0.1:4455")
	}
	got := buildFake(t, proxy)
	have := map[string]bool{}
	for _, s := range got.Secrets {
		have[string(s)] = true
	}
	for _, name := range []string{"HTTPS_PROXY", "HTTP_PROXY", "https_proxy", "http_proxy"} {
		if !have["pw-"+name] {
			t.Fatalf("Secrets lacks the credential carried by %s (%d values)", name, len(got.Secrets))
		}
	}
}

// TestBuildDriverEnvMasksProxyCredentialAsWritten: a credential written with
// percent escapes is listed in the spelling the child sees, not only decoded.
func TestBuildDriverEnvMasksProxyCredentialAsWritten(t *testing.T) {
	proxy := sixProxy()
	proxy[1] = "HTTP_PROXY=http://cascade:%70%77%2Dx@127.0.0.1:4455/path"
	got := buildFake(t, proxy)
	have := map[string]bool{}
	for _, s := range got.Secrets {
		have[string(s)] = true
	}
	for _, want := range []string{"pw-x", "%70%77%2Dx"} {
		if !have[want] {
			t.Fatalf("Secrets lacks the credential spelled %q (%d values)", want, len(got.Secrets))
		}
	}
}

// TestBuildDriverEnvRefusesUnreadableProxyURL: a URL variable the scrub could
// not read a credential from (no scheme, bad escape) is refused, not injected.
func TestBuildDriverEnvRefusesUnreadableProxyURL(t *testing.T) {
	for name, val := range map[string]string{
		"schemeless": "cascade:sch-secret-1@127.0.0.1:1",
		"bad escape": "http://cascade:esc-secret-2%ZZ@127.0.0.1:1",
		"no host":    "http://",
	} {
		proxy := sixProxy()
		proxy[1] = "HTTP_PROXY=" + val
		_, err := BuildDriverEnv(fakeBinding("fake"), fakeAuth(), testProfile(), "j", "tok", "lease", proxy, hostile)
		if !cascade.HasKind(err, cascade.KindInvalidInput) || !strings.Contains(err.Error(), "HTTP_PROXY") {
			t.Fatalf("%s: err = %v, want KindInvalidInput naming HTTP_PROXY", name, err)
		}
		if strings.Contains(err.Error(), "secret") {
			t.Fatalf("%s: the error echoes the value: %v", name, err)
		}
	}
}

// TestBuildDriverEnvMasksUsernameOnlyProxyToken: a URL with a username and no
// password carries a token; it is listed for the scrub.
func TestBuildDriverEnvMasksUsernameOnlyProxyToken(t *testing.T) {
	proxy := sixProxy()
	proxy[1] = "HTTP_PROXY=http://tok-only-3@127.0.0.1:4455"
	got := buildFake(t, proxy)
	for _, s := range got.Secrets {
		if string(s) == "tok-only-3" {
			return
		}
	}
	t.Fatalf("Secrets lacks the username-only token (%d values)", len(got.Secrets))
}

// TestBuildDriverEnvMasksEscapedUsernameOnlyToken: a token written with
// percent escapes and no colon is listed decoded and as written.
func TestBuildDriverEnvMasksEscapedUsernameOnlyToken(t *testing.T) {
	proxy := sixProxy()
	proxy[1] = "HTTP_PROXY=http://tok%2Donly@127.0.0.1:4455"
	got := buildFake(t, proxy)
	have := map[string]bool{}
	for _, s := range got.Secrets {
		have[string(s)] = true
	}
	for _, want := range []string{"tok-only", "tok%2Donly"} {
		if !have[want] {
			t.Fatalf("Secrets lacks the token spelled %q (%d values)", want, len(got.Secrets))
		}
	}
}
