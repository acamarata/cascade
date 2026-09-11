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
// P1-E19-W4-S41-T1 added filippo.io/age (the REFERENCE age encryption
// implementation the backup pipeline's crypto.go encrypts/decrypts chunks
// with - Art.2 forbids a hand-rolled cipher) and github.com/klauspost/compress
// (pure-Go zstd - internal/backup/crypto.go's Compress/Decompress stage, no
// CGO per 06 §2). Both verified against their module-cache LICENSE file:
// BSD-3-Clause. Neither pulled in a new transitive dependency (`go mod tidy`
// added no further require lines).
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
//
// P1-E14-W3-S30-T6 (wazero host-ABI parity spike) added
// github.com/tetratelabs/wazero (the wazero adapter in
// internal/plugins/adapter_wazero_spike_test.go loads a real compiled
// wasm module through the real library, per Art.2 - a hand-rolled
// interpreter would not satisfy the external-contract requirement).
// Verified against the module-cache LICENSE file: Apache-2.0,
// allowlisted (same license family as github.com/spf13/cobra above). No
// new transitive dependency: wazero has no runtime dependencies of its
// own (`go list -m all` shows none beyond the stdlib).
var KnownModuleLicenses = map[string]string{
	"filippo.io/age":                "BSD-3-Clause",
	"github.com/klauspost/compress": "BSD-3-Clause",
	"mvdan.cc/sh/v3":                "BSD-3-Clause",
	"github.com/godbus/dbus/v5":     "BSD-2-Clause",
	"golang.org/x/crypto":           "BSD-3-Clause",
	// Verified against the LICENSE file in the module cache at
	// v4.10.0 ("The MIT License (MIT)", Copyright (c) 2014 Bob Matcuk),
	// not inferred from the module path or from a package index. Pulled in
	// by internal/jobs/glob.go for lease-scope pattern matching.
	"github.com/bmatcuk/doublestar/v4":     "MIT",
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
	"github.com/tetratelabs/wazero":        "Apache-2.0",
	// Verified against the module-cache LICENSE file: the standard
	// three-clause Go Authors text, including the "Neither the name of
	// Google LLC" clause that distinguishes BSD-3 from BSD-2. Pulled in by
	// internal/repo/detector_go.go, which parses a real go.mod to detect a
	// Go module; the file's own header calls it the license-gated addition.
	// Note the comment at the top of THIS file, which said modfile "would
	// itself be a new dependency" back when the license gate was written.
	// It is one now, deliberately, and this is the entry that gates it.
	"golang.org/x/mod": "BSD-3-Clause",
	// gopkg.in/yaml.v3 ships a dual license: the files ported from libyaml
	// (apic.go, emitterc.go, parserc.go, readerc.go, scannerc.go, writerc.go,
	// yamlh.go, yamlprivateh.go) stay under their original MIT, and the rest
	// is Apache-2.0. Both halves are on LicenseAllowlist. The registry maps
	// one identifier per module, so the more restrictive of the two is
	// recorded here; verified against the module's own LICENSE file.
	"gopkg.in/yaml.v3":     "Apache-2.0",
	"modernc.org/libc":     "BSD-3-Clause",
	"modernc.org/mathutil": "BSD-3-Clause",
	"modernc.org/memory":   "BSD-3-Clause",
	"modernc.org/sqlite":   "BSD-3-Clause",
	// P1-E17-W4-S38-T4 added github.com/jackc/pgx/v5 (the pure-Go, no-CGO
	// Postgres wire driver providers/postgres and providers/pgvector are
	// built on, replacing the B/S-03.T5 stub) and its transitive closure:
	// github.com/jackc/pgpassfile, github.com/jackc/pgservicefile,
	// github.com/jackc/puddle/v2, golang.org/x/sync, golang.org/x/text.
	// Verified against each module's module-cache LICENSE file: the three
	// jackc/* modules are MIT (same author/header as jackc/pgconn family
	// already common in the Go Postgres ecosystem); golang.org/x/sync and
	// golang.org/x/text carry the same standard Go Authors BSD-3-Clause
	// text as golang.org/x/sys and golang.org/x/mod above.
	"github.com/jackc/pgx/v5":        "MIT",
	"github.com/jackc/pgpassfile":    "MIT",
	"github.com/jackc/pgservicefile": "MIT",
	"github.com/jackc/puddle/v2":     "MIT",
	"golang.org/x/sync":              "BSD-3-Clause",
	"golang.org/x/text":              "BSD-3-Clause",
	// golang.org/x/tools (P1-E33-W7-S67-T3, 06-FORGE-SPEC §7 license-gated
	// standing-authorized addition): internal/repo/graph_go.go's real Go
	// symbol-graph extractor uses golang.org/x/tools/go/packages. Verified
	// against the module-cache LICENSE file: the same standard Go Authors
	// three-clause text as golang.org/x/sys/x/mod/x/sync/x/crypto above.
	// `go mod tidy` also bumped x/mod, x/sys and x/sync (already
	// registered) to versions x/tools requires; no new transitive module
	// path was introduced.
	"golang.org/x/tools": "BSD-3-Clause",
	// P1-E17-W4-S38-T6 added github.com/redis/go-redis/v9 (providers/redis's
	// wire client) and its transitive closure, plus
	// github.com/alicebob/miniredis/v2 (a real-RESP, pure-Go in-memory
	// Redis, _test.go-only, untagged coverage lane; Art.2's real-server
	// docker lane is separate). Verified per-module LICENSE file:
	"github.com/redis/go-redis/v9":     "BSD-2-Clause",
	"github.com/cespare/xxhash/v2":     "MIT",
	"go.uber.org/atomic":               "MIT",
	"github.com/alicebob/miniredis/v2": "MIT",
	"github.com/yuin/gopher-lua":       "MIT",
	// P1-E17-W4-S38-T7 added github.com/minio/minio-go/v7 (providers/s3's
	// wire client) and its transitive closure. Verified per-module
	// LICENSE file:
	"github.com/minio/minio-go/v7": "Apache-2.0",
	"github.com/minio/crc64nvme":   "Apache-2.0",
	"github.com/minio/md5-simd":    "Apache-2.0",
	"gopkg.in/ini.v1":              "Apache-2.0",
	"github.com/klauspost/crc32":   "BSD-3-Clause",
	"golang.org/x/net":             "BSD-3-Clause",
	"github.com/philhofer/fwd":     "MIT",
	"github.com/rs/xid":            "MIT",
	"github.com/tinylib/msgp":      "MIT",
	"github.com/zeebo/xxh3":        "BSD-2-Clause",
	// go.yaml.in/yaml/v3: same dual MIT/Apache-2.0 split as gopkg.in/yaml.v3
	// above (successor namespace); more-restrictive half recorded here.
	"go.yaml.in/yaml/v3": "Apache-2.0",
	// P1-E17-W4-S38-T7 also added github.com/johannesboyne/gofakes3 (a
	// real S3 REST API over an in-memory backend, _test.go-only, untagged
	// coverage lane) and its ACTUAL (module-graph-pruned) closure.
	// gofakes3's own go.mod additionally names
	// aws-sdk-go-v2/afero/bbolt/mgo.v2/testify — dependencies of
	// gofakes3's OWN _test.go files only; `go mod tidy` correctly never
	// added them here (verified absent above), so they carry no
	// license-gate obligation. Verified per-module LICENSE file:
	"github.com/johannesboyne/gofakes3": "MIT",
	"github.com/ryszard/goskiplist":     "Apache-2.0",
	"go.shabbyrobe.org/gocovmerge":      "BSD-2-Clause",
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
