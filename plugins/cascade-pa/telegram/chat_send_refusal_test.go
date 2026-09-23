// Purpose (this file): the POSIX proof for S-49.T4's fix to S-48.T2's Opus
//   CR gap Q1 (silent loss): chat.go's old Send returned nil after
//   guardOutbound swapped a secret-shaped body for the value-free refusal
//   text, so a caller believed a refused message was delivered. Skipped on
//   Windows (runtime.GOOS guard, matching TestChatBridge_StartStop in
//   chat_test.go): there Send refuses at the platform gate before ever
//   reaching the secret gate this file exercises (chat_windows_test.go
//   proves that half).
//
// SPORT: plugins/cascade-pa/telegram TestChatBridge_Send_SecretRefused/TEST
//   (P1-E23-W5-S49-T4).

package telegram

import (
	"context"
	"runtime"
	"strings"
	"testing"

	cascadepa "github.com/acamarata/cascade/plugins/cascade-pa"

	"github.com/acamarata/cascade/pkg/cascade"
)

// TestChatBridge_Send_SecretRefused proves a secret-shaped body given to
// Send can never look delivered: Send must return the typed refusal
// (errSendBodyRefused, KindPolicyDenied), and no wire body recorded by the
// fake transport — refused or otherwise — may ever carry the witness.
func TestChatBridge_Send_SecretRefused(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Send refuses at the platform gate first on Windows (chat_windows_test.go)")
	}
	r := newBridgeRig(t)
	r.module.secretScanner = secretScannerFor(witnessSecretText, "api-key")
	threadID := threadIDForChat(9011)
	r.privacy.set(threadID, cascadepa.TierPublic)

	err := r.bridge.Send(context.Background(), threadID, []byte(witnessSecretText))
	if err == nil {
		t.Fatal("Send with a secret-shaped body succeeded, want a refusal")
	}
	if !cascade.HasKind(err, cascade.KindPolicyDenied) {
		t.Fatalf("Send returned %v, want KindPolicyDenied", err)
	}
	if len(r.doer.methods()) != 0 {
		t.Fatalf("a refused Send still reached the transport: %v", r.doer.methods())
	}
	for _, sent := range r.doer.sentTexts() {
		if strings.Contains(sent, witnessSecretText) {
			t.Fatalf("the witness secret text reached the wire: %q", sent)
		}
	}
}
