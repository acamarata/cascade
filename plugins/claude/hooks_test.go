package claude

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withRenderer swaps RenderHookPack for the duration of a test.
func withRenderer(t *testing.T, r HookPackRendererFunc) {
	t.Helper()
	prev := RenderHookPack
	RenderHookPack = r
	t.Cleanup(func() { RenderHookPack = prev })
}

// rendersSocket returns a renderer echoing the socket it was handed, so a
// test can prove the socket actually reached the rendered output.
func rendersSocket() HookPackRendererFunc {
	return func(socket string) ([]byte, error) {
		return []byte(`{"hooks":{"SessionStart":[{"command":"` + socket + `"}]}}`), nil
	}
}

func TestInstallHookPackWritesConfigAndVersion(t *testing.T) {
	paths := tempPaths(t)
	withRenderer(t, rendersSocket())

	results, err := installHookPackAt(paths, "/run/cascade.sock")
	if err != nil {
		t.Fatalf("install hook pack: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("results = %+v, want the config and its version companion", results)
	}
	for _, res := range results {
		if !res.Changed {
			t.Fatalf("%s reported no change on first install", res.Path)
		}
	}

	config, err := os.ReadFile(filepath.Join(paths.HookConfig, hookPackFile)) //nolint:gosec // test-local temp path.
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(config), "/run/cascade.sock") {
		t.Fatalf("installed config does not carry the socket it was rendered for:\n%s", config)
	}
	version, err := os.ReadFile(filepath.Join(paths.HookConfig, hookPackVersionFile)) //nolint:gosec // test-local temp path.
	if err != nil {
		t.Fatal(err)
	}
	if string(version) != HookPackVersion {
		t.Fatalf("installed version = %q, want %q", version, HookPackVersion)
	}
}

func TestInstallHookPackIsIdempotent(t *testing.T) {
	paths := tempPaths(t)
	withRenderer(t, rendersSocket())
	if _, err := installHookPackAt(paths, "/run/cascade.sock"); err != nil {
		t.Fatal(err)
	}
	second, err := installHookPackAt(paths, "/run/cascade.sock")
	if err != nil {
		t.Fatal(err)
	}
	for _, res := range second {
		if res.Changed {
			t.Fatalf("second install rewrote %s; the install is not converging", res.Path)
		}
	}
}

// TestInstallHookPackRewritesOnSocketChange proves the content comparison
// documented on InstallHookPack is real: a version check alone would
// decline to reinstall after the daemon socket moved, leaving a pack whose
// commands post to a socket that no longer exists.
func TestInstallHookPackRewritesOnSocketChange(t *testing.T) {
	paths := tempPaths(t)
	withRenderer(t, rendersSocket())
	if _, err := installHookPackAt(paths, "/run/old.sock"); err != nil {
		t.Fatal(err)
	}
	second, err := installHookPackAt(paths, "/run/new.sock")
	if err != nil {
		t.Fatal(err)
	}
	if !second[0].Changed {
		t.Fatal("the hook pack was not reinstalled after the socket changed")
	}
	config, err := os.ReadFile(filepath.Join(paths.HookConfig, hookPackFile)) //nolint:gosec // test-local temp path.
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(config), "/run/new.sock") {
		t.Fatalf("installed config still names the old socket:\n%s", config)
	}
}

// TestInstallHookPackRefusesEmptyRender proves the fail-closed contract:
// the host registry returns nil rather than a partial config, and
// installing nothing silently is exactly what must not happen.
func TestInstallHookPackRefusesEmptyRender(t *testing.T) {
	paths := tempPaths(t)
	withRenderer(t, func(string) ([]byte, error) { return nil, nil })
	_, err := installHookPackAt(paths, "/run/cascade.sock")
	if err == nil {
		t.Fatal("install with an empty render succeeded, want a refusal")
	}
	if _, statErr := os.Stat(filepath.Join(paths.HookConfig, hookPackFile)); !os.IsNotExist(statErr) {
		t.Fatal("a refused install still wrote a hook config")
	}
}

func TestUnwiredRendererIsARealError(t *testing.T) {
	withRenderer(t, unwiredRenderer)
	_, err := installHookPackAt(tempPaths(t), "/run/cascade.sock")
	if err == nil || !strings.Contains(err.Error(), "not wired") {
		t.Fatalf("error = %v, want it to say the renderer is not wired", err)
	}
}

func TestInstallHookPackPropagatesRendererError(t *testing.T) {
	sentinel := errors.New("renderer exploded")
	withRenderer(t, func(string) ([]byte, error) { return nil, sentinel })
	if _, err := installHookPackAt(tempPaths(t), "/s"); !errors.Is(err, sentinel) {
		t.Fatalf("error = %v, want it to wrap the renderer's error", err)
	}
}

func TestSetHookPackRendererRefusesNil(t *testing.T) {
	if err := SetHookPackRenderer(nil); err == nil {
		t.Fatal("SetHookPackRenderer(nil) succeeded, want a refusal")
	}
}

// TestSocketPathRequiresTheVariable proves an unset CASCADE_SOCKET is a
// refusal, never a guessed default: a pack installed against a guessed
// socket fails silently at session time, which is the worst outcome.
func TestSocketPathRequiresTheVariable(t *testing.T) {
	if _, err := SocketPath(envMap(nil)); err == nil {
		t.Fatal("SocketPath with CASCADE_SOCKET unset succeeded, want a refusal")
	}
	if _, err := SocketPath(nil); err == nil {
		t.Fatal("SocketPath with a nil Env succeeded, want a refusal")
	}
	got, err := SocketPath(envMap(map[string]string{"CASCADE_SOCKET": "/run/c.sock"}))
	if err != nil || got != "/run/c.sock" {
		t.Fatalf("SocketPath = %q, %v; want /run/c.sock", got, err)
	}
}

// TestInstallHookPackReadsTheRealSocketVariable covers the production
// entry point, which resolves CASCADE_SOCKET itself rather than taking it
// as an argument.
func TestInstallHookPackReadsTheRealSocketVariable(t *testing.T) {
	paths := tempPaths(t)
	withRenderer(t, rendersSocket())
	t.Setenv("CASCADE_SOCKET", "/run/from-the-environment.sock")

	results, err := InstallHookPack(paths)
	if err != nil {
		t.Fatalf("InstallHookPack: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("results = %+v, want the config and its version companion", results)
	}
	config, err := os.ReadFile(filepath.Join(paths.HookConfig, hookPackFile)) //nolint:gosec // test-local temp path.
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(config), "/run/from-the-environment.sock") {
		t.Fatalf("installed config did not use the socket from the environment:\n%s", config)
	}
}

// TestInstallHookPackRefusesWithoutTheSocketVariable proves the production
// entry point fails closed rather than installing against a guess.
func TestInstallHookPackRefusesWithoutTheSocketVariable(t *testing.T) {
	withRenderer(t, rendersSocket())
	t.Setenv("CASCADE_SOCKET", "")
	if _, err := InstallHookPack(tempPaths(t)); err == nil {
		t.Fatal("InstallHookPack succeeded with CASCADE_SOCKET unset, want a refusal")
	}
}

func TestSetHookPackRendererAcceptsARealRenderer(t *testing.T) {
	prev := RenderHookPack
	t.Cleanup(func() { RenderHookPack = prev })
	if err := SetHookPackRenderer(rendersSocket()); err != nil {
		t.Fatalf("SetHookPackRenderer: %v", err)
	}
}
