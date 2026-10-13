//go:build !windows

package claude

// Purpose: repo-owned harness settings (T0 decision F4): a leased tree whose
//   .claude/ holds a settings file the hook pack does not write is refused
//   before any harness process runs, and the job argv loads only project
//   settings (--setting-sources project), so the pack's settings.json is
//   the only settings file the harness reads.
// Constraints: SCOPE DEVIATION (declared): not in files_scope.add; tests
//   guard.go refuseForeignSettings and protocol.go buildArgv. The planted
//   hook would touch a marker outside the tree if anything ran it.
// SPORT: providers.agents.claude/ADD (P1-E30-W6-S61-T2).

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// wraps reports whether target is err itself or anywhere in its chain, by
// identity (errors.Is on a cascade error compares only the Kind).
func wraps(err, target error) bool {
	for ; err != nil; err = errors.Unwrap(err) {
		if err == target {
			return true
		}
	}
	return false
}

func commandHook(marker string) string {
	return `{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"touch ` + marker + `"}]}]}}` + "\n"
}

func TestClaudeRefusesRepoHarnessSettings(t *testing.T) {
	cases := map[string]struct {
		file     string
		dropPack bool
	}{
		"settings.local.json with a command hook": {"settings.local.json", false},
		"upper-case Settings.Local.JSON":          {"Settings.Local.JSON", false},
		"settings.json the pack does not write":   {"settings.json", true},
	}
	for label, c := range cases {
		t.Run(label, func(t *testing.T) {
			f := newFx(t)
			if c.dropPack {
				if err := os.Remove(filepath.Join(f.pack, "settings.json")); err != nil {
					t.Fatal(err)
				}
			}
			marker := filepath.Join(f.dir, "hook-ran")
			mustMkdir(t, filepath.Join(f.root, ".claude"))
			mustWrite(t, filepath.Join(f.root, ".claude", c.file), commandHook(marker))
			_, err := f.driver(f.config()).Spawn(context.Background(), f.spec())
			if !wraps(err, ErrForeignHarnessSettings) || exists(f.anyRan) || exists(marker) {
				t.Fatalf("Spawn = %v (harness ran %v, hook ran %v): repo settings must refuse before any process",
					err, exists(f.anyRan), exists(marker))
			}
		})
	}
	t.Run("the pack's own settings.json replaces the repo's and only project settings load", func(t *testing.T) {
		f := newFx(t)
		mustMkdir(t, filepath.Join(f.root, ".claude"))
		mustWrite(t, filepath.Join(f.root, ".claude", "settings.json"), commandHook(filepath.Join(f.dir, "hook-ran")))
		if _, _, err := run(t, f.driver(f.config()), f.spec()); err != nil {
			t.Fatal(err)
		}
		got, _ := os.ReadFile(filepath.Join(f.root, ".claude", "settings.json"))
		if string(got) != "{}\n" || !hasPair(argvOf(t, f), "--setting-sources", "project") {
			t.Fatalf("settings.json %q, argv %q: the pack's settings and --setting-sources project must win", got, argvOf(t, f))
		}
	})
}
