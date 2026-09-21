// Purpose: re-run the adversarial review's own real-filesystem inputs
//
//	against the REAL detector, the real filesystem and whatever nself is
//	installed on the machine — the only way to prove the two blocking
//	defects are gone rather than merely unit-tested away. The positive
//	control is here for the same reason: three negative cases would all
//	pass on a detector that never detects anything.
//
// WHY THIS IS ENV-GATED. It reads the real home directory, the real PATH
//
//	and forks the real CLI, so it is not hermetic and must never run as part
//	of the ordinary suite (internal/build/sweep_test.go's CASCADE_HYGIENE_RUN
//	gate is the same pattern for the same reason). Every case still ASSERTS:
//	this is a gated test, not a printout.
//
// Run it with:
//
//	CASCADE_NSELF_REAL_PROBE=1 \
//	CASCADE_NSELF_REAL_PROJECT=<a real nself project dir> \
//	go test -count=1 -run TestDetect_RealFilesystemProbes -v ./plugins/nself/
//
// SPORT: plugins/nself detect (TEST) — P1-E25-W5-S52-T2.

package nself

import (
	"context"
	"os"
	"strings"
	"testing"
)

// realProbeGate is the environment variable that enables this file.
const realProbeGate = "CASCADE_NSELF_REAL_PROBE"

// realProjectVar names a real nself project directory, for the positive
// control and for the case that proves the probe is told which directory to
// inspect.
const realProjectVar = "CASCADE_NSELF_REAL_PROJECT"

func TestDetect_RealFilesystemProbes(t *testing.T) {
	if os.Getenv(realProbeGate) != "1" {
		t.Skip(realProbeGate + " not set; the real-filesystem/real-CLI probe is not requested")
	}
	t.Run("root=/private/tmp with nself installed", probeNonProjectTmp)
	t.Run("root under $HOME while $HOME/.nself exists", probeUnderHome)
	t.Run("root=a real nself project (POSITIVE CONTROL)", probeRealProject)
	t.Run("root_dir=/tmp/empty while the working directory is a project", probeEmptyRootFromProjectCwd)
}

// probeNonProjectTmp is the review's first input: a directory that is not a
// project, on a machine where nself IS installed. The draft errored here.
func probeNonProjectTmp(t *testing.T) {
	res := newDetector("/private/tmp").Detect(context.Background())
	t.Logf("result: %+v", res)
	if res.Detected {
		t.Fatalf("Detect(/private/tmp) = %+v, want detected=false", res)
	}
	if res.ScanErr != nil {
		t.Fatalf("Detect(/private/tmp) ScanErr = %v, want nil", res.ScanErr)
	}
}

// probeUnderHome is the review's second input: a directory under the home
// directory while $HOME/.nself (nself's global state dir) exists. The draft
// reported detected=true here for every such directory.
func probeUnderHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("os.UserHomeDir: %v", err)
	}
	exists, isDir, serr := osStatFS{}.Stat(home + "/" + markerDirName)
	t.Logf("$HOME/%s exists=%v isDir=%v err=%v", markerDirName, exists, isDir, serr)
	res := newDetector(home + "/Downloads").Detect(context.Background())
	t.Logf("result: %+v", res)
	if res.Detected {
		t.Fatalf("Detect($HOME/Downloads) = %+v, want detected=false", res)
	}
}

// probeRealProject is the positive control.
func probeRealProject(t *testing.T) {
	project := os.Getenv(realProjectVar)
	if project == "" {
		t.Skip(realProjectVar + " not set; no real project to detect")
	}
	res := newDetector(project).Detect(context.Background())
	t.Logf("result: %+v", res)
	if !res.Detected || res.Method != "marker-dir" {
		t.Fatalf("Detect(%s) = %+v, want a marker-dir detection", project, res)
	}
	if !strings.HasSuffix(res.MarkerPath, markerDirName) {
		t.Fatalf("MarkerPath = %q, want the .nself directory that matched", res.MarkerPath)
	}
}

// probeEmptyRootFromProjectCwd is the review's third input: the probe must
// run in root_dir, not in the process's working directory.
func probeEmptyRootFromProjectCwd(t *testing.T) {
	project := os.Getenv(realProjectVar)
	if project == "" {
		t.Skip(realProjectVar + " not set; cannot place the working directory in a real project")
	}
	if err := os.MkdirAll("/tmp/empty", 0o755); err != nil {
		t.Fatalf("mkdir /tmp/empty: %v", err)
	}
	t.Chdir(project)
	res := newDetector("/tmp/empty").Detect(context.Background())
	t.Logf("cwd=%s result: %+v", project, res)
	if res.Detected {
		t.Fatalf("Detect(/tmp/empty) from a project cwd = %+v, want detected=false "+
			"(the probe must run IN /tmp/empty, not in the working directory)", res)
	}
	if res.ProbeErr == nil {
		t.Fatal("Detect(/tmp/empty) ProbeErr = nil, want the typed outcome of a probe that ran there")
	}
}
