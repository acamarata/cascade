package build

import (
	"debug/buildinfo"
	"fmt"
	"runtime/debug"
	"slices"
	"strings"
	"unicode"

	"gopkg.in/yaml.v3"
)

// DevkeysInBuildInfo reports whether the embedded -tags setting enables devkeys.
// Purpose: keep development custody out of releases. Inputs: Go build settings.
// Outputs: exact tag membership. Constraints: no binary string search.
// SPORT: release artifact gate.
func DevkeysInBuildInfo(bi *debug.BuildInfo) bool {
	if bi == nil {
		return false
	}
	for _, setting := range bi.Settings {
		if setting.Key == "-tags" && slices.Contains(strings.FieldsFunc(setting.Value, tagSeparator), "devkeys") {
			return true
		}
	}
	return false
}

func tagSeparator(r rune) bool {
	return r == ',' || unicode.IsSpace(r)
}

// CheckNoDevkeys refuses artifacts with devkeys or unreadable Go build settings.
// Purpose: fail closed at release time. Inputs: built artifact path.
// Outputs: rejection error or nil. Constraints: uses the Go build-info reader.
// SPORT: release artifact gate.
func CheckNoDevkeys(path string) error {
	bi, err := buildinfo.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read artifact build info: %w", err)
	}
	if DevkeysInBuildInfo(bi) {
		return fmt.Errorf("artifact %q enables devkeys", path)
	}
	return nil
}

// buildSettings holds the release build fields that can carry Go build tags.
type buildSettings struct {
	Tags  []string `yaml:"tags"`
	Flags []string `yaml:"flags"`
	Env   []string `yaml:"env"`
}

// checkGoreleaserNoDevkeys validates the release configuration used by the gate.
// Purpose: reject unsafe build settings and missing artifact checks.
// Inputs: release YAML. Outputs: first violation. Constraints: require builds.
// SPORT: release configuration gate.
func checkGoreleaserNoDevkeys(data []byte) error {
	var config struct {
		Env    []string `yaml:"env"`
		Builds []struct {
			ID            string `yaml:"id"`
			Tool          string `yaml:"tool"`
			GoBinary      string `yaml:"gobinary"`
			buildSettings `yaml:",inline"`
			Overrides     []buildSettings `yaml:"overrides"`
			Hooks         struct {
				Post []string `yaml:"post"`
			} `yaml:"hooks"`
		} `yaml:"builds"`
	}
	if err := yaml.Unmarshal(data, &config); err != nil {
		return fmt.Errorf("release config: %w", err)
	}
	if len(config.Builds) == 0 {
		return fmt.Errorf("release config has no builds")
	}
	for _, b := range config.Builds {
		// A wrapper compiler could add tags the static check never sees.
		for _, tool := range []string{b.Tool, b.GoBinary} {
			if tool != "" && tool != "go" {
				return fmt.Errorf("release build %q uses compiler wrapper %q", b.ID, tool)
			}
		}
		settings := append([]buildSettings{b.buildSettings, {Env: config.Env}}, b.Overrides...)
		for _, set := range settings {
			if settingsTemplated(set) {
				return fmt.Errorf("release build %q templates its tags, flags or GOFLAGS", b.ID)
			}
			if settingsEnableDevkeys(set.Tags, set.Flags, set.Env) {
				return fmt.Errorf("release build %q enables devkeys", b.ID)
			}
		}
		// The gate must run last so no later hook can replace the checked artifact.
		if n := len(b.Hooks.Post); n == 0 || b.Hooks.Post[n-1] != `go run ./internal/build/nodevkeys "{{ .Path }}"` {
			return fmt.Errorf("release build %q lacks the nodevkeys artifact hook as its last post hook", b.ID)
		}
	}
	return nil
}

// settingsTemplated reports template expressions that a static check cannot
// resolve: any in tags or flags, any in an environment key (which can render
// to GOFLAGS), and any in a GOFLAGS environment value.
func settingsTemplated(set buildSettings) bool {
	for _, value := range slices.Concat(set.Tags, set.Flags) {
		if strings.Contains(value, "{{") {
			return true
		}
	}
	for _, value := range set.Env {
		key, _, _ := strings.Cut(value, "=")
		if strings.Contains(key, "{{") {
			return true
		}
		if strings.TrimSpace(key) == "GOFLAGS" && strings.Contains(value, "{{") {
			return true
		}
	}
	return false
}

// settingsEnableDevkeys reports whether any value names the exact devkeys tag
// once split on tag, assignment and quote separators.
func settingsEnableDevkeys(lists ...[]string) bool {
	for _, values := range lists {
		for _, value := range values {
			tokens := strings.FieldsFunc(value, func(r rune) bool {
				return tagSeparator(r) || r == '=' || r == '\'' || r == '"'
			})
			if slices.Contains(tokens, "devkeys") {
				return true
			}
		}
	}
	return false
}
