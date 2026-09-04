// Package build (this file) holds the dependency-license allowlist gate
// from P1-E01-W1-S01-T3 (12-QUALITY-CONSTITUTION.md Art.10 supply-chain
// clause; dependency-rules.md #2 "no GPL/LGPL/AGPL dependencies"). It is a
// self-contained Go implementation - no new module dependency is added
// (R-14.115 forbids a concurrent dependency-adding ticket) - so it runs
// identically in CI (`.github/workflows/supply-chain.yml`, which also runs
// the real `go-licenses` tool as a belt-and-braces check) and locally via
// `go test ./internal/build/...` without requiring any extra tool install.
//
// Design: go.mod's require directives are parsed directly (a hand-rolled
// parser - golang.org/x/mod/modfile would itself be a new dependency), then
// every module path is looked up in a maintained registry mapping it to its
// verified SPDX license identifier. A module missing from the registry is
// UNKNOWN (fails closed); a module present but not on the allowlist is a
// named violation (e.g. copyleft). The registry is updated in the same
// change that adds or upgrades a dependency (R-14.115), verified against
// the dependency's actual LICENSE file or its GitHub API license field.
package build

import (
	"bufio"
	"io"
	"sort"
	"strings"
)

// LicenseAllowlist is the permissive-license set this repo accepts
// (dependency-rules.md #2): MIT, BSD-2-Clause, BSD-3-Clause, Apache-2.0,
// ISC, CC0-1.0. Anything else - GPL/LGPL/AGPL/SSPL or an unrecognised
// license - fails the gate.
var LicenseAllowlist = map[string]bool{
	"MIT":          true,
	"BSD-2-Clause": true,
	"BSD-3-Clause": true,
	"Apache-2.0":   true,
	"ISC":          true,
	"CC0-1.0":      true,
}

// KnownModuleLicenses is the maintained registry of every module path this
// module currently depends on (direct and indirect), mapped to its
// verified SPDX identifier. Verified 2026-09-02 against each module's
// GitHub license API / LICENSE file. Update this map in the same change
// that adds or upgrades a require line in go.mod.
//
// P1-E02-W1-S02-T2 (R-14.130) added modernc.org/sqlite (the pure-Go, no-CGO
// SQLite driver - §2/02-TARGET-STRUCTURE mandate) and its transitive
// closure: modernc.org/libc, modernc.org/mathutil, modernc.org/memory,
// golang.org/x/sys (promoted direct - the §D-3 linux flock path calls
// unix.Flock), github.com/dustin/go-humanize, github.com/google/uuid,
// github.com/mattn/go-isatty, github.com/ncruces/go-strftime,
// github.com/remyoudompheng/bigfft. Every one verified against its
// module-cache LICENSE file - all BSD-3-Clause or MIT, both allowlisted.
//
// P1-E02-W1-S02-T4 added github.com/zeebo/blake3 (providers/fs's BLAKE3-256
// content-addressing hash - see providers/fs/blobstore.go's package doc)
// and its one runtime dependency github.com/klauspost/cpuid/v2 (CPU
// feature detection blake3 uses for its SIMD fast paths). Verified against
// each module's module-cache LICENSE file: zeebo/blake3 is public domain /
// CC0-1.0, klauspost/cpuid/v2 is MIT - both allowlisted.
//
// The vault ticket (internal/secrets) added github.com/godbus/dbus/v5 (the
// pure-Go D-Bus client the linux secret-service custody backend speaks, so
// no libsecret cgo binding is needed) and golang.org/x/crypto (the
// XChaCha20-Poly1305/ChaCha20-Poly1305 AEAD and scrypt KDF the encrypted
// file-vault fallback uses to write real age v1 files). Verified against
// each module's module-cache LICENSE file: godbus/dbus is BSD-2-Clause,
// golang.org/x/crypto is BSD-3-Clause; both allowlisted. Neither added a
// new transitive dependency (x/crypto's golang.org/x/sys was already
// direct).
//
// P1-E03-W1-S05-T8 added github.com/fsnotify/fsnotify (internal/runtime's
// hot-reload config.toml watcher, hotreload_watch.go) - this ticket was
// the sole dependency-adding ticket in flight per R-14.115. Verified
// against the module-cache LICENSE file: fsnotify is BSD-3-Clause
// (Copyright (c) 2012 The Go Authors / fsnotify Authors), allowlisted; it
// added no new transitive dependency (golang.org/x/sys was already
// direct).
var KnownModuleLicenses = map[string]string{
	"mvdan.cc/sh/v3":                       "BSD-3-Clause",
	"github.com/godbus/dbus/v5":            "BSD-2-Clause",
	"golang.org/x/crypto":                  "BSD-3-Clause",
	"github.com/pelletier/go-toml/v2":      "MIT",
	"github.com/spf13/cobra":               "Apache-2.0",
	"github.com/inconshreveable/mousetrap": "Apache-2.0",
	"github.com/spf13/pflag":               "BSD-3-Clause",
	"github.com/dustin/go-humanize":        "MIT",
	"github.com/fsnotify/fsnotify":         "BSD-3-Clause",
	"github.com/google/uuid":               "BSD-3-Clause",
	"github.com/klauspost/cpuid/v2":        "MIT",
	"github.com/mattn/go-isatty":           "MIT",
	"github.com/ncruces/go-strftime":       "MIT",
	"github.com/remyoudompheng/bigfft":     "BSD-3-Clause",
	"github.com/zeebo/blake3":              "CC0-1.0",
	"golang.org/x/sys":                     "BSD-3-Clause",
	"modernc.org/libc":                     "BSD-3-Clause",
	"modernc.org/mathutil":                 "BSD-3-Clause",
	"modernc.org/memory":                   "BSD-3-Clause",
	"modernc.org/sqlite":                   "BSD-3-Clause",
}

// LicenseDependency is one require-directive entry parsed from a go.mod:
// its module path and declared version (version is carried for reporting
// only - the registry is keyed by path).
type LicenseDependency struct {
	Path    string
	Version string
}

// LicenseViolation describes one dependency that failed the allowlist gate.
type LicenseViolation struct {
	Module  string
	License string // "" when the module has no registry entry at all
	Reason  string
}

// ParseGoModRequires extracts every require-directive module path and
// version from go.mod content, covering both the block form:
//
//	require (
//		module v1.2.3
//		module2 v4.5.6 // indirect
//	)
//
// and the single-line form ("require module v1.2.3"). It ignores module/
// go/toolchain/replace/exclude directives and blank/comment lines. It never
// touches the filesystem or network - callers pass file bytes directly, so
// both the real go.mod and seeded-violation fixtures use the same path.
func ParseGoModRequires(data []byte) ([]LicenseDependency, error) {
	var deps []LicenseDependency
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	inBlock := false
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "//") {
			continue
		}
		if inBlock {
			if line == ")" {
				inBlock = false
				continue
			}
			if d, ok := parseRequireEntry(line); ok {
				deps = append(deps, d)
			}
			continue
		}
		switch {
		case line == "require (":
			inBlock = true
		case strings.HasPrefix(line, "require "):
			rest := strings.TrimSpace(strings.TrimPrefix(line, "require"))
			if d, ok := parseRequireEntry(rest); ok {
				deps = append(deps, d)
			}
		}
	}
	if err := scanner.Err(); err != nil && err != io.EOF {
		return nil, err
	}
	sort.Slice(deps, func(i, j int) bool { return deps[i].Path < deps[j].Path })
	return deps, nil
}

// parseRequireEntry parses one "module version [// indirect]" entry.
func parseRequireEntry(s string) (LicenseDependency, bool) {
	s = strings.TrimSpace(s)
	if idx := strings.Index(s, "//"); idx >= 0 {
		s = strings.TrimSpace(s[:idx])
	}
	fields := strings.Fields(s)
	if len(fields) < 2 {
		return LicenseDependency{}, false
	}
	return LicenseDependency{Path: fields[0], Version: fields[1]}, true
}

// CheckLicenses evaluates deps against registry and LicenseAllowlist,
// returning a sorted violation for every dependency that is unregistered
// or whose registered license is not on the allowlist. A nil/empty result
// means every dependency is permissively licensed.
func CheckLicenses(deps []LicenseDependency, registry map[string]string) []LicenseViolation {
	var out []LicenseViolation
	for _, d := range deps {
		lic, ok := registry[d.Path]
		switch {
		case !ok:
			out = append(out, LicenseViolation{Module: d.Path, Reason: "no registry entry (unknown license)"})
		case !LicenseAllowlist[lic]:
			out = append(out, LicenseViolation{Module: d.Path, License: lic, Reason: "license not on allowlist"})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Module < out[j].Module })
	return out
}
