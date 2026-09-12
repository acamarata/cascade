package cascadepa

import (
	"context"
	"errors"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
	pacmd "github.com/acamarata/cascade/plugins/cascade-pa/cmd"
)

// TestChatPluginAbsent proves handlers.RunCommand only ever recognizes
// commandChat ("chat"): any other name is a typed KindNotFound error —
// there is no fallback dispatch that would let a caller reach chat-shaped
// behavior under a different name. "chat" itself reaches the host through
// cmd/cascade/builtin_plugins.go's direct mount (not this manifest, which
// declares no commands — see the package doc comment), so with
// cascade-pa absent from a binary's import graph entirely, neither that
// mount nor this dispatch exist at all.
func TestChatPluginAbsent(t *testing.T) {
	err := handlers{}.RunCommand(context.Background(), "not-a-real-command", nil)
	if err == nil {
		t.Fatal("RunCommand: want an error for an unregistered command, got nil")
	}
	kind, ok := cascade.KindOf(err)
	if !ok || kind != cascade.KindNotFound {
		t.Fatalf("RunCommand: Kind = %v (ok=%v), want KindNotFound", kind, ok)
	}
}

// TestManifestDeclaresNoCommands locks in that cascade-pa's manifest-v2
// provides.commands surface is EMPTY: "chat" is a reserved core noun
// (pkg/plugin/validate.go rule R5) that BuiltinRegistry.Load rejects on
// collision if declared here, so `cascade chat` is mounted directly on
// root by cmd/cascade/builtin_plugins.go instead (see the package doc
// comment and FIX-manifest-collision-and-conductor-seam). A future
// manifest command addition must pick a name outside reservedCommandNames.
func TestManifestDeclaresNoCommands(t *testing.T) {
	m := manifest()
	if m.ID != pluginID {
		t.Fatalf("manifest ID = %q, want %q", m.ID, pluginID)
	}
	if m.Runtime != plugin.RuntimeBuiltin {
		t.Fatalf("manifest Runtime = %v, want RuntimeBuiltin", m.Runtime)
	}
	if len(m.Provides.Commands) != 0 {
		t.Fatalf("Provides.Commands = %+v, want none (chat mounts directly, not via manifest)", m.Provides.Commands)
	}
}

// TestRunCommandDispatchesChatToRealCobraCommand proves RunCommand("chat",
// args) is wired to pacmd.NewChatCommand's real logic, not a stub: with no
// Client configured, the daemon-unreachable error surfaces through
// exactly this call path, end to end from the BuiltinHandlers interface.
func TestRunCommandDispatchesChatToRealCobraCommand(t *testing.T) {
	pacmd.SetClient(nil)
	t.Cleanup(func() { pacmd.SetClient(nil) })

	err := handlers{}.RunCommand(context.Background(), commandChat, []string{"hello"})
	if err == nil {
		t.Fatal("RunCommand(chat, [hello]): want the daemon-unreachable error, got nil")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindUnavailable {
		t.Fatalf("RunCommand: Kind = %v (ok=%v), want KindUnavailable", kind, ok)
	}
}

// TestRunCommandChatSuccess proves a configured Client makes RunCommand
// succeed end to end (mutation-proof pairing for the test above: this one
// shows GREEN when the wiring is intact).
func TestRunCommandChatSuccess(t *testing.T) {
	pacmd.SetClient(fakeOneShotClient{})
	t.Cleanup(func() { pacmd.SetClient(nil) })

	if err := (handlers{}).RunCommand(context.Background(), commandChat, []string{"hello"}); err != nil {
		t.Fatalf("RunCommand(chat, [hello]): %v", err)
	}
}

type fakeOneShotClient struct{}

func (fakeOneShotClient) OneShot(context.Context, pacmd.OneShotRequest) (pacmd.OneShotResult, error) {
	return pacmd.OneShotResult{TurnID: "t1", ThreadID: "th1", Content: "ok"}, nil
}

func (fakeOneShotClient) Stream(context.Context, pacmd.OneShotRequest) (<-chan string, <-chan error) {
	tokens := make(chan string)
	errs := make(chan error)
	close(tokens)
	close(errs)
	return tokens, errs
}

// TestRunCommandDispatchesInChatSoulEdit proves RunCommand("chat", ...)
// consults the in-chat command registry BEFORE building the ordinary chat
// cobra command: a `/soul edit` positional prompt reaches
// HandleSoulChatCommand and never reaches the LLM Client at all (no
// Client is configured here, so a fall-through would surface
// errClientUnconfigured instead of a soul-edit confirmation).
func TestRunCommandDispatchesInChatSoulEdit(t *testing.T) {
	pacmd.SetClient(nil)
	t.Cleanup(func() { pacmd.SetClient(nil) })
	SetSoulChatClient(&stubSoulChatClient{editRes: SoulChatEditResult{Version: 7}})
	t.Cleanup(func() { SetSoulChatClient(nil) })

	err := handlers{}.RunCommand(context.Background(), commandChat, []string{"/soul edit body hi"})
	if err != nil {
		t.Fatalf("RunCommand(chat, [/soul edit body hi]): %v", err)
	}
}

// TestRunCommandFallsThroughForOrdinaryPrompt is the mutation-proof
// pairing: an ordinary prompt that does not match any registered in-chat
// command still reaches the real chat cobra command (proven by the
// daemon-unreachable error only that path produces).
func TestRunCommandFallsThroughForOrdinaryPrompt(t *testing.T) {
	pacmd.SetClient(nil)
	t.Cleanup(func() { pacmd.SetClient(nil) })

	err := handlers{}.RunCommand(context.Background(), commandChat, []string{"hello"})
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindUnavailable {
		t.Fatalf("RunCommand(chat, [hello]): Kind = %v (ok=%v), want KindUnavailable (fall-through to the LLM client)", kind, ok)
	}
}

// TestDispatchToolAndIntentUnsupported: cascade-pa provides no tools or
// intents in this ticket; both dispatch paths return typed KindNotFound
// errors, never a panic and never a silent empty success.
func TestDispatchToolAndIntentUnsupported(t *testing.T) {
	if _, err := (handlers{}).DispatchTool(context.Background(), "anything", nil); !errors.As(err, new(*cascade.Error)) {
		t.Fatalf("DispatchTool: err = %v, want a *cascade.Error", err)
	}
	if _, err := (handlers{}).DispatchIntent(context.Background(), "anything", nil); !errors.As(err, new(*cascade.Error)) {
		t.Fatalf("DispatchIntent: err = %v, want a *cascade.Error", err)
	}
}
