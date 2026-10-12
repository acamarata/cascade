//go:build widgetparity

package acceptance

// Purpose (this file): the LIVE widget parity check — the verifier run over
//   the owner's real journal for the release candidate under test. It is the
//   check P1-EXIT-15's condition 11 runs.
// Inputs (all three required; none has a default):
//   WIDGET_PARITY_JOURNAL   path to the filled journal (evidence paths in it
//                           are relative to its directory)
//   WIDGET_PARITY_RC_TAG    the rc tag every row must carry
//   WIDGET_PARITY_ARTIFACT  path to the installed .app or the .dmg; its
//                           sha256, signature and
//                           Gatekeeper assessment are recomputed here
// Outputs: a pass, or one failure per problem naming the locator.
// Constraints: NO SKIP PATH. An unset value, a missing artifact or a missing
//   journal FAILS: a release lane that cannot run the check has not passed it.
//   The CDHash, the Developer ID authority and the spctl verdict come from
//   codesign and spctl run on the artifact, never from the journal.
// Run: go test -tags widgetparity -run '^TestWidgetParityJournal_Live$' ./internal/acceptance/
// Contract: contract:widget-parity-journal (P1-WID-10).

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// liveEnvNames are the three required environment values.
var liveEnvNames = []string{"WIDGET_PARITY_JOURNAL", "WIDGET_PARITY_RC_TAG", "WIDGET_PARITY_ARTIFACT"}

// liveCodesignTimeout bounds one codesign run.
const liveCodesignTimeout = 60 * time.Second

// liveCodesign returns codesign's own report on the artifact.
func liveCodesign(artifact string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), liveCodesignTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "codesign", "-d", "--verbose=4", artifact).CombinedOutput()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// liveSpctl returns Gatekeeper's assessment of the artifact. A rejection
// still returns its text, so the verifier can name it.
func liveSpctl(artifact string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), liveCodesignTimeout)
	defer cancel()
	args := []string{"-a", "-vv"}
	if strings.HasSuffix(filepath.Clean(artifact), ".dmg") {
		args = append(args, "-t", "open", "--context", "context:primary-signature")
	}
	out, err := exec.CommandContext(ctx, "spctl", append(args, artifact)...).CombinedOutput()
	if len(out) == 0 {
		return "", err
	}
	return string(out), nil
}

func TestWidgetParityJournal_Live(t *testing.T) {
	var unset []string
	vals := map[string]string{}
	for _, name := range liveEnvNames {
		vals[name] = strings.TrimSpace(os.Getenv(name))
		if vals[name] == "" {
			unset = append(unset, name)
		}
	}
	if len(unset) > 0 {
		t.Fatalf("widget parity check cannot run: %s not set (a release lane that cannot run it has not passed it)", strings.Join(unset, ", "))
	}
	artifact := vals["WIDGET_PARITY_ARTIFACT"]
	if _, err := os.Stat(artifact); err != nil {
		t.Fatalf("WIDGET_PARITY_ARTIFACT %q does not exist: %v", artifact, err)
	}
	res := verifyParityJournal(vals["WIDGET_PARITY_JOURNAL"], parityEnv{
		rcTag:      vals["WIDGET_PARITY_RC_TAG"],
		artifact:   artifact,
		codesignOf: liveCodesign,
		spctlOf:    liveSpctl,
	})
	for _, p := range res.problems {
		t.Error(p)
	}
	t.Logf("passed %v; disclosed-not-passed %v; deferred-P2 %v", res.passed, res.disclosed, res.deferred)
	if len(res.passed) == 0 {
		t.Error("no row passed")
	}
}
