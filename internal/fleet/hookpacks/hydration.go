package hookpacks

// Purpose: the prompt-hydration hook pack (P1-E16-W4-S34-T4, R-16.6a) —
//   one UserPromptSubmit descriptor that runs `cascade context slice
//   --hook` and injects the resulting capsule into the harness's prompt.
// Inputs: none (the descriptor is a declaration).
// Outputs: a HookPack the composition root registers when
//   [context.hydration].enabled resolves true.
// Constraints: unlike the sessions pack's fire-and-forget POSTs, the
//   harness WAITS on this hook and reads its stdout, so the descriptor
//   states its own timeout rather than inheriting the harness default.
//   The command is the stable installed binary name, never a source or
//   build-tree path (P/S-34.T1's resolution rule), and it carries no
//   socket placeholder: the hook process decides for itself whether a
//   daemon is reachable, exactly as every other daemonless-capable verb
//   does.
// SPORT: internal/fleet/hookpacks hydration pack (ADD) — P1-E16-W4-S34-T4.

// HydrationPackName is the registry key the hydration pack installs under.
const HydrationPackName = "hydration"

// HydrationTimeoutSeconds is R-16.6a's fixed 3-second bound, stated in the
// harness's own units. It is the harness-side half of the same bound the
// hook enforces internally: the hook returns nothing and exits 0 on its
// own timeout, and this is what stops a hook that hangs BELOW that (a
// wedged syscall, a stalled disk) from holding the user's prompt open.
const HydrationTimeoutSeconds = 3

// HydrationCommand is the command the descriptor installs.
//
// `cascade` by name, resolved through PATH at hook time: an absolute path
// into a source or build tree is the v1 failure P/S-34.T1 names, since it
// breaks the moment the tree moves.
const HydrationCommand = "cascade context slice --hook"

// HydrationPack returns the UserPromptSubmit descriptor.
//
// UserPromptSubmit is backed by a real captured fixture
// (testdata/cc-hook-fixtures/userpromptsubmit.json), which is what makes
// registering it legitimate under this package's own rule that no event
// type is supported without one. SessionsPack's doc comment records that
// rule; this pack is the first to satisfy it for a fourth event type.
func HydrationPack() HookPack {
	return HookPack{
		Name: HydrationPackName,
		Descriptors: []HookDescriptor{{
			EventType:       EventUserPromptSubmit,
			Matcher:         "",
			CommandTemplate: HydrationCommand,
			TimeoutSeconds:  HydrationTimeoutSeconds,
		}},
	}
}
