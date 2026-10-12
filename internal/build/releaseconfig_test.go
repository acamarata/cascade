// Purpose: shape gates for the release train (contract:signed-release-artifacts,
// docs/developer/release-train.md). They parse .goreleaser.yaml and release.yml;
// each check is proven by a seeded violation that must turn it red. No network.
package build

import (
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

var (
	relStagingSecret = "${{ secrets.CASCADE_STAGING_REMOTE }}"
	relWorkflowPath  = ".github/workflows/release.yml"
	relExpr          = regexp.MustCompile(`(?s)\$\{\{.*?\}\}`)
	relBindLine      = `[ "$GITHUB_SHA" = "$COMMIT_SHA" ] || (echo "commit_sha is not the dispatched commit" && exit 1)`
	relStagingRef    = regexp.MustCompile(`\$\{?CASCADE_STAGING_REMOTE`)
	relStagingUses   = []string{`[ -n "${CASCADE_STAGING_REMOTE:-}" ]`, `[[ "$CASCADE_STAGING_REMOTE" =~ `, `ref="ghcr.io/${CASCADE_STAGING_REMOTE}"`}
	relNoCache       = map[string]string{"actions/setup-go@": "cache", "docker/setup-qemu-action@": "cache-image", "docker/setup-buildx-action@": "cache-binary"}
)

type relConfig struct {
	Builds []struct {
		ID           string
		Env, Ldflags []string
		Hooks        struct{ Post []string }
	} `yaml:"builds"`
	Archives, Dockers []struct{ IDs []string }
	Checksum          struct {
		Name  string                  `yaml:"name_template"`
		Extra []struct{ Glob string } `yaml:"extra_files"`
	} `yaml:"checksum"`
	Signs []struct {
		Cmd, Artifacts string
		Args           []string
	} `yaml:"signs"`
	Casks []struct {
		Repository struct{ Owner, Name string }
		Binaries   []string
		Caveats    string
		URL        struct{ Template string }
	} `yaml:"homebrew_casks"`
	Release struct {
		GitHub struct{ Owner, Name string } `yaml:"github"`
		Extra  []struct{ Glob string }      `yaml:"extra_files"`
	} `yaml:"release"`
}

type (
	relStep struct {
		Uses, Run string
		Env, With map[string]any
	}
	relJob struct {
		If          string
		Permissions map[string]string
		Env         map[string]any
		Steps       []relStep
	}
	relWorkflow struct {
		On          map[string]map[string]any
		Permissions map[string]string
		Jobs        map[string]relJob
	}
	relCheck []string // violation messages
)

func (c *relCheck) bad(ok bool, msg string) {
	if !ok {
		*c = append(*c, msg)
	}
}

// relSeeds: check passes on the tracked file and reports want per seed; each seed matches exactly once.
func relSeeds(t *testing.T, path string, check func(*testing.T, string) []string, seeds map[string][2]string) {
	t.Helper()
	text := relRead(t, path)
	if v := check(t, text); len(v) != 0 {
		t.Fatalf("%s drifts from the contract: %q", path, v)
	}
	for want, seed := range seeds {
		if strings.Count(text, seed[0]) != 1 {
			t.Fatalf("seed %q does not match exactly once in %s", seed[0], path)
		}
		if v := check(t, strings.Replace(text, seed[0], seed[1], 1)); !slices.ContainsFunc(v, func(s string) bool { return strings.Contains(s, want) }) {
			t.Errorf("seeded violation %q stayed green: %q", want, v)
		}
	}
}

func relRead(t *testing.T, rel string) string {
	b, _ := os.ReadFile(filepath.Join(sweepModuleRoot(t), rel))
	if len(b) == 0 {
		t.Fatalf("%s is empty or unreadable", rel)
	}
	return string(b)
}

func relDecode[T any](t *testing.T, text string) (v T) {
	if err := yaml.Unmarshal([]byte(text), &v); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return v
}

// releaseConfigViolations lists every way .goreleaser.yaml text drifts from the contract.
func releaseConfigViolations(t *testing.T, text string) []string {
	cfg := relDecode[relConfig](t, text)
	key := strings.Fields(relRead(t, "docs/keys/minisign.pub")) // the last field is the public key itself
	var c relCheck
	bad := c.bad // short alias: this function is a list of checks
	bad(len(cfg.Signs) == 1 && cfg.Signs[0].Cmd == "minisign" && cfg.Signs[0].Artifacts == "checksum", "want one minisign sign over the checksum")
	for _, sg := range cfg.Signs {
		i := slices.Index(sg.Args, "-t")
		bad(i >= 0 && i+1 < len(sg.Args) && sg.Args[i+1] == "cascade {{ .Version }} checksums", "minisign -t is not "+"cascade {{ .Version }} checksums")
	}
	bad(cfg.Checksum.Name == "{{ .ProjectName }}_checksums.txt", "checksum name is not the version-free cascade_checksums.txt")
	installSh := func(f struct{ Glob string }) bool { return strings.TrimPrefix(f.Glob, "./") == "install.sh" }
	bad(slices.ContainsFunc(cfg.Checksum.Extra, installSh) && slices.ContainsFunc(cfg.Release.Extra, installSh), "checksum or release extra_files lack install.sh")
	ids := []string{}
	for _, b := range cfg.Builds {
		ids = append(ids, b.ID)
		bad(slices.Contains(b.Env, "CGO_ENABLED=0"), "build "+b.ID+" lacks CGO_ENABLED=0")
		bad(slices.Contains(b.Hooks.Post, `go run ./internal/build/nodevkeys "{{ .Path }}"`), "build "+b.ID+" lacks the nodevkeys post hook")
		bad(!slices.ContainsFunc(b.Ldflags, func(l string) bool { return strings.Contains(l, "InstallChannel") }), "build "+b.ID+" stamps InstallChannel")
	}
	bad(slices.Equal(ids, []string{"cascade", "cascade-github"}), "builds are not [cascade cascade-github]")
	for _, a := range append(cfg.Archives, cfg.Dockers...) {
		bad(slices.Contains(a.IDs, "cascade") && slices.Contains(a.IDs, "cascade-github"), "an archive or image lacks cascade-github")
	}
	bad(len(cfg.Archives) > 0 && len(cfg.Dockers) == 2 && len(cfg.Casks) == 1, "archives, dockers or the one cask missing")
	for _, k := range cfg.Casks {
		bad(k.Repository.Owner == "acamarata" && k.Repository.Name == "homebrew-cascade", "cask tap is not acamarata/homebrew-cascade")
		bad(slices.Equal(k.Binaries, []string{"cascade", "cascade-github"}), "cask binaries are not [cascade cascade-github]")
		bad(strings.Contains(k.Caveats, "https://acamarata.github.io/cascade/keys/minisign.pub") && strings.Contains(k.Caveats, "docs/keys/minisign.pub"), "cask caveats do not name the key locations")
		bad(!strings.Contains(k.Caveats, key[len(key)-1]), "cask caveats inline the public key")
		bad(strings.HasPrefix(k.URL.Template, "https://github.com/acamarata/cascade/releases/download/"), "cask url does not pin the public release repository")
	}
	bad(cfg.Release.GitHub.Owner == `{{ index (split .Env.CASCADE_STAGING_REMOTE "/") 0 }}` && cfg.Release.GitHub.Name == `{{ index (split .Env.CASCADE_STAGING_REMOTE "/") 1 }}`, "release.github is not split from CASCADE_STAGING_REMOTE")
	return c
}

func TestReleaseConfigShape(t *testing.T) {
	relSeeds(t, ".goreleaser.yaml", releaseConfigViolations, map[string][2]string{
		"checksum name is not":      {`"{{ .ProjectName }}_checksums.txt"`, `"{{ .ProjectName }}_{{ .Tag }}_checksums.txt"`},
		"stamps InstallChannel":     {"buildinfo.Date={{ .Date }}\n  #", "buildinfo.Date={{ .Date }}\n      - -X github.com/acamarata/cascade/internal/buildinfo.InstallChannel=script\n  #"},
		"cask tap is not":           {"name: homebrew-cascade", "name: homebrew-tap"},
		"lacks the nodevkeys":       {"  - id: cascade-github\n    hooks:\n      post:\n        - 'go run ./internal/build/nodevkeys \"{{ .Path }}\"'\n", "  - id: cascade-github\n"},
		"an archive or image lacks": {"      - cascade\n      - cascade-github\n    formats:", "      - cascade\n    formats:"},
		"cask url does not pin":     {"https://github.com/acamarata/cascade/releases/download/{{ .Tag }}", "https://github.com/x/y/releases/download/{{ .Tag }}"},
		"release.github is not":     {`'{{ index (split .Env.CASCADE_STAGING_REMOTE "/") 0 }}'`, "acamarata"},
	})
}

func TestReleaseConfigSignsTrustedCommentVersion(t *testing.T) {
	relSeeds(t, ".goreleaser.yaml", releaseConfigViolations, map[string][2]string{
		"-t is not":           {"      - \"-t\"\n      - \"cascade {{ .Version }} checksums\"\n", ""},
		"-t is not cascade":   {`"cascade {{ .Version }} checksums"`, `"cascade checksums"`},
		"-t is not cascade {": {`"cascade {{ .Version }} checksums"`, `"cascade {{ .Version }} checksums (snapshot)"`},
	})
}

// workflowViolations lists release.yml drift: triggers, secrets, permissions, snapshot keys, rc-publish.
func workflowViolations(t *testing.T, text string) []string {
	var c relCheck
	wf := relDecode[relWorkflow](t, text)
	c.bad(slices.Equal(slices.Sorted(maps.Keys(wf.On)), []string{"push", "workflow_dispatch"}), "triggers are not exactly push and workflow_dispatch")
	c.bad(wf.On["push"]["tags"] == nil && wf.On["push"]["tags-ignore"] == nil, "a push of tags triggers the workflow")
	inputs, _ := wf.On["workflow_dispatch"]["inputs"].(map[string]any)
	c.bad(slices.Equal(slices.Sorted(maps.Keys(inputs)), []string{"commit_sha", "final_tag", "notes", "record_json"}), "dispatch inputs are not exactly final_tag, commit_sha, record_json and notes")
	c.bad(!strings.Contains(text, "vars.") && !strings.Contains(text, "::add-mask::"), "vars. or ::add-mask:: appears")
	c.bad(!regexp.MustCompile(`git\s+push`).MatchString(text), "a step runs git push")
	c.bad(!strings.Contains(text, "secrets.CASCADE_MINISIGN_KEY") && !strings.Contains(text, "secrets.CASCADE_MINISIGN_PUBKEY"), "a repository secret holds a minisign key")
	for _, e := range relExpr.FindAllString(text, -1) {
		c.bad(!strings.Contains(e, "CASCADE_STAGING_REMOTE") || e == relStagingSecret, "staging remote read from "+e)
		c.bad(!strings.Contains(e, "secrets") || e == relStagingSecret, "workflow reads secret "+e)
	}
	c.bad(len(wf.Permissions) == 1 && wf.Permissions["contents"] == "read", "workflow default permissions are not contents read")
	for name, job := range wf.Jobs {
		c.bad(relPermsExact(name, job), "job "+name+" widens permissions (permissions are not exactly the per-job allowlist)")
		c.bad(relPromotionJobs[name].If == "" || job.If == relPromotionJobs[name].If, name+" is not dispatch-only with its record_json gate")
		c.bad(job.Env["CASCADE_STAGING_REMOTE"] == nil || job.Env["CASCADE_STAGING_REMOTE"] == "example-owner/example-repo", "job "+name+" env holds a staging remote other than the placeholder")
		for _, s := range job.Steps {
			c.bad(name == "rc-publish" || !strings.Contains(s.Run, "goreleaser release") || strings.Contains(s.Run, "--snapshot"), "job "+name+" runs a non-snapshot release")
			c.bad(relPromotionJobs[name].If != "" || !slices.Contains(slices.Collect(maps.Values(s.Env)), any(relStagingSecret)), "job "+name+" step env holds the staging secret")
		}
	}
	for _, name := range []string{"snapshot", "install-e2e"} {
		gen := slices.IndexFunc(wf.Jobs[name].Steps, func(s relStep) bool { return strings.Contains(s.Run, "minisign -G -W") })
		for i, s := range wf.Jobs[name].Steps {
			c.bad(s.Env["CASCADE_MINISIGN_KEY"] == nil || (gen >= 0 && gen < i && strings.HasPrefix(fmt.Sprint(s.Env["CASCADE_MINISIGN_KEY"]), "${{ runner.temp }}/")), name+" signs with a key it did not generate")
		}
		c.bad(gen >= 0, name+" generates no minisign key")
	}
	c.bad(strings.Count(text, relStagingSecret) == relStagingHolders(wf), "the staging secret reaches a job, key or place other than rc-publish and promote step env")
	rcJobViolations(wf.Jobs["rc-publish"], &c)
	return append(c, relLooseKeys(t, text)...)
}

// rcJobViolations checks the rc-publish job itself.
func rcJobViolations(job relJob, c *relCheck) {
	find, has := func(f func(relStep) bool) int { return slices.IndexFunc(job.Steps, f) }, strings.Contains
	refuse := find(func(s relStep) bool {
		return has(s.Run, "exit 1") && s.Env["CASCADE_STAGING_REMOTE"] == relStagingSecret
	})
	checkout := find(func(s relStep) bool { return strings.HasPrefix(s.Uses, "actions/checkout@") })
	current := find(func(s relStep) bool {
		return s.Env["GORELEASER_CURRENT_TAG"] == "${{ inputs.final_tag }}" && has(s.Run, "--skip=publish,sign")
	})
	oras := find(func(s relStep) bool { return has(s.Run, "oras push") && has(s.Run, `exec 3>&1 > "${RUNNER_TEMP}/`) })
	for _, s := range job.Steps {
		c.bad(!strings.Contains(s.Uses, "upload-artifact") && !strings.HasPrefix(s.Uses, "actions/cache"), "rc-publish uses "+s.Uses)
		c.bad(!strings.Contains(s.Uses+s.Run, "cosign") && !strings.Contains(s.Uses+s.Run, "attest-build-provenance"), "rc-publish signs or attests")
		c.bad(!strings.Contains(s.Run, "--snapshot") && !strings.Contains(s.Run, "GORELEASER_CURRENT_TAG="), "rc-publish overrides the version or builds a snapshot")
		c.bad(len(relStagingRef.FindAllString(s.Run, -1)) == strings.Count(s.Run, relStagingUses[0])+strings.Count(s.Run, relStagingUses[1])+strings.Count(s.Run, relStagingUses[2]), "rc-publish uses the staging secret outside its fixed checks")
		for prefix, input := range relNoCache {
			c.bad(!strings.HasPrefix(s.Uses, prefix) || s.With[input] == false, "rc-publish leaves "+input+" on for "+s.Uses)
		}
	}
	c.bad(refuse >= 0 && checkout > refuse, "the refusal step does not precede checkout")
	bind := find(func(s relStep) bool { return strings.Contains("\n"+s.Run, "\n"+relBindLine+"\n") })
	c.bad(bind > checkout && bind < current, "commit_sha is not bound to the dispatched commit between checkout and build")
	c.bad(current >= 0, "GORELEASER_CURRENT_TAG is not inputs.final_tag on the --skip=publish,sign build")
	c.bad(oras >= 0, "oras push is missing or its output is not kept in the runner")
}

func TestReleaseWorkflowRcPublishShape(t *testing.T) {
	oras, accepted := "      - name: Set up oras\n", `echo "rc-publish inputs accepted"`
	relSeeds(t, relWorkflowPath, workflowViolations, map[string][2]string{
		"rc-publish uses":             {oras, "      - uses: actions/upload-artifact@ea165f8d65b6e75b540449e92b4886f43607fa02 # v4.6.2\n" + oras},
		"leaves cache on":             {"nothing it builds may persist beyond the run.\n      - uses: actions/setup-go@40f1582b2485089dde7abd97c1529aa768e1baff # v5.6.0\n        with:\n          go-version-file: go.mod\n          cache: false\n", "nothing it builds may persist beyond the run.\n      - uses: actions/setup-go@40f1582b2485089dde7abd97c1529aa768e1baff # v5.6.0\n        with:\n          go-version-file: go.mod\n"},
		"dispatch inputs":             {"      commit_sha:\n", "      rc_tag:\n        type: string\n      commit_sha:\n"},
		"permissions are not":         {"      contents: read\n      packages: write\n", "      contents: read\n      packages: write\n      id-token: write\n"},
		"default permissions":         {"permissions:\n  contents: read\n", "permissions:\n  contents: write\n"},
		"job verify widens":           {"    name: goreleaser check + build (no secrets)\n", "    name: goreleaser check + build (no secrets)\n    permissions:\n      packages: write\n"},
		"non-snapshot release":        {"run: goreleaser release --snapshot --clean --skip=docker,sbom", "run: goreleaser release --clean --skip=docker,sbom"},
		"vars. or ::add-mask::":       {"refuse() { echo \"rc-publish", "echo \"::add-mask::$FINAL_TAG\"\n          refuse() { echo \"rc-publish"},
		"git push":                    {"git tag \"$FINAL_TAG\" \"$COMMIT_SHA\"", "git tag \"$FINAL_TAG\" \"$COMMIT_SHA\" && git push origin \"$FINAL_TAG\""},
		"a push of tags":              {"      - p1-integration\n", "      - p1-integration\n    tags:\n      - 'v*'\n"},
		"signs or attests":            {oras, "      - run: cosign version\n" + oras},
		"refusal step does not":       {"      # Runs before checkout.", "      - uses: actions/checkout@11d5960a326750d5838078e36cf38b85af677262 # v4.4.0\n      # Runs before checkout."},
		"staging remote read from":    {"CASCADE_STAGING_REMOTE: ${{ secrets.CASCADE_STAGING_REMOTE }}\n          FINAL_TAG: ${{ inputs.final_tag }}\n          COMMIT_SHA: ${{ inputs.commit_sha }}\n        run: |\n          set -u", "CASCADE_STAGING_REMOTE: ${{ vars.CASCADE_STAGING_REMOTE }}\n          FINAL_TAG: ${{ inputs.final_tag }}\n          COMMIT_SHA: ${{ inputs.commit_sha }}\n        run: |\n          set -u"},
		"outside its fixed checks":    {accepted, `echo "${CASCADE_STAGING_REMOTE#*/}" "$CASCADE_STAGING_REMOTE" | cut -d/ -f2`},
		"not bound to the dispatched": {relBindLine, `[ "$GITHUB_SHA" = "$COMMIT_SHA" ] || true`},
		"workflow reads secret":       {"        run: goreleaser check\n", "        env:\n          STG: ${{\n            secrets[format('CASCADE_STAGING_{0}', 'REMOTE')] }}\n        run: goreleaser check\n"},
		"reaches a job":               {"      - name: goreleaser check\n        run: goreleaser check\n", "      - name: goreleaser check\n        env:\n          X: ${{ secrets.CASCADE_STAGING_REMOTE }}\n        run: goreleaser check\n"},
		"overrides the version":       {"run: goreleaser release --clean --skip=publish,sign", "run: GORELEASER_CURRENT_TAG=v2.0.0-rc.1 goreleaser release --clean --skip=publish,sign"},
		"GORELEASER_CURRENT_TAG":      {"run: goreleaser release --clean --skip=publish,sign", "run: goreleaser release --clean --skip=publish"},
		"not kept in the runner":      {`exec 3>&1 > "${RUNNER_TEMP}/rc-publish-push.log" 2>&1`, ""},
	})
}

// TestReleaseWorkflowRefusalStepRuns runs the refusal step's own body: bad input exits 1, output never holds the secret.
func TestReleaseWorkflowRefusalStepRuns(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil || filepath.Separator == '\\' {
		t.Skip("needs a POSIX bash (windows may resolve the WSL launcher)")
	}
	sec, sha := "zz-owner/zz-stagingprobe", strings.Repeat("a", 40)
	for _, c := range [][4]string{{"", "v2.0.0", sha, "1"}, {sec, "v2.0.0-rc.1", sha, "1"}, {sec, "v3.0.0", sha, "1"}, {sec, "v2.0", sha, "1"},
		{sec, "v2.0.0", sha[:39], "1"}, {sec, "v2.0.0\nx", sha, "1"}, {"Zz/Upper", "v2.0.0", sha, "1"}, {sec, "v2.0.0", sha, "0"}} {
		cmd := exec.Command("bash", "-c", relDecode[relWorkflow](t, relRead(t, relWorkflowPath)).Jobs["rc-publish"].Steps[0].Run)
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "CASCADE_STAGING_REMOTE=" + c[0], "FINAL_TAG=" + c[1], "COMMIT_SHA=" + c[2]}
		if out, _ := cmd.CombinedOutput(); fmt.Sprint(cmd.ProcessState.ExitCode()) != c[3] || strings.Contains(string(out), "stagingprobe") || strings.Contains(string(out), "zz-owner") {
			t.Errorf("case %q: exit %d, output %q", c, cmd.ProcessState.ExitCode(), out)
		}
	}
}

func TestReleaseWorkflowNeverLeaksStaging(t *testing.T) {
	hits := func(name string) []string { // tracked files containing name; the placeholder probe below proves the scan works
		out, _ := exec.Command("git", "-C", sweepModuleRoot(t), "grep", "-l", "-F", "-e", name).Output()
		return strings.Fields(string(out))
	}
	name := os.Getenv("CASCADE_STAGING_REMOTE")
	if name == "" || name == "example-owner/example-repo" {
		t.Skip("CASCADE_STAGING_REMOTE is not the real staging name here; the ship identifier sweep covers it")
	}
	if !slices.Contains(hits("example-owner/example-repo"), relWorkflowPath) {
		t.Fatal("the scan misses the placeholder it must see in release.yml")
	}
	if h := append(hits(name), hits(name[strings.LastIndex(name, "/")+1:])...); len(h) != 0 {
		t.Errorf("tracked files name the staging remote or its repository part: %q", h)
	}
}

func TestReleaseWorkflowSnapshotUsesEphemeralKey(t *testing.T) {
	relSeeds(t, relWorkflowPath, workflowViolations, map[string][2]string{
		"secret holds":          {"CASCADE_MINISIGN_KEY: ${{ runner.temp }}/ephemeral.key", "CASCADE_MINISIGN_KEY: ${{ secrets.CASCADE_MINISIGN_KEY }}"},
		"snapshot generates no": {`minisign -G -W -s "${RUNNER_TEMP}/ephemeral.key"`, `cp /dev/null "${RUNNER_TEMP}/ephemeral.key"`},
	})
}
