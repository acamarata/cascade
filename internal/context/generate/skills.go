package generate

// Purpose: the data half of RenderAgentsSkills and the all-or-nothing
//   write. templateValues turns a View into the closed set of template
//   values; fillBody puts them into a compiled template body;
//   WriteAgentsSkills checks every target before the Writer touches one.
// Inputs: a View; the Rendered files and a Writer.
// Outputs: the filled bodies; one Outcome per file.
// Constraints: every value is inline code (inertCode): it cannot open a
//   block, close its span, start a line, forge a marker or form a "!`"
//   command. A body naming an unknown value is KindIntegrity. A mangled
//   marker in an existing target or an unwritable target directory is a
//   typed error before anything is written.
// SPORT: context-engine/agents-skills-generator (ADD, P1-GEN-08).

import (
	"context"
	"errors"
	"io/fs"
	"os"
	pathpkg "path"
	"path/filepath"
	"strings"

	"github.com/acamarata/cascade/internal/context/policy"
	"github.com/acamarata/cascade/pkg/cascade"
)

// The texts a value list renders as when the view holds nothing for it.
const (
	noneDetected = "none detected"
	noFacts      = "none beyond the detected inventory"
)

// templateValues maps each placeholder name to its rendered text.
func templateValues(v View) (map[string]string, error) {
	var langs, builds, tests, lints, facts []string
	for _, s := range v.Scopes {
		for _, l := range s.Languages {
			langs, builds = appendNew(langs, l.Language), appendNew(builds, l.Build)
			tests, lints = appendNew(tests, l.Test), appendNew(lints, l.Lint)
		}
		for _, f := range s.Facts {
			facts = appendNew(facts, string(f.Subject)+": "+f.Value)
		}
	}
	if len(builds)+len(tests)+len(lints) == 0 {
		return nil, cascade.New(cascade.KindInvalidInput, "generate: the inventory names no build, test or lint command")
	}
	return map[string]string{
		"languages":      codeList(langs, noneDetected),
		"build":          codeList(builds, noneDetected),
		"test":           codeList(tests, noneDetected),
		"lint":           codeList(lints, noneDetected),
		"facts":          codeList(facts, noFacts),
		"kernel_version": inertCode(policy.KernelVersion),
	}, nil
}

// appendNew appends s when it is non-empty and not yet in list.
func appendNew(list []string, s string) []string {
	if s == "" {
		return list
	}
	for _, have := range list {
		if have == s {
			return list
		}
	}
	return append(list, s)
}

// codeList renders values as inline code joined by ", ", or none.
func codeList(values []string, none string) string {
	if len(values) == 0 {
		return none
	}
	parts := make([]string, len(values))
	for i, s := range values {
		parts[i] = inertCode(s)
	}
	return strings.Join(parts, ", ")
}

// inertCode is inlineCode with one more guard: a "!" directly before a
// backtick gets a space, so no value can form a "!`command`" line.
func inertCode(s string) string {
	s = strings.ReplaceAll(s, "!`", "! `")
	if strings.HasSuffix(s, "!") {
		s += " "
	}
	return inlineCode(s)
}

// fillBody replaces every {{name}} in body with vals[name].
func fillBody(src string, body []byte, vals map[string]string) ([]byte, error) {
	var b strings.Builder
	rest := string(body)
	for {
		i := strings.Index(rest, "{{")
		if i < 0 {
			b.WriteString(rest)
			return []byte(b.String()), nil
		}
		j := strings.Index(rest[i:], "}}")
		if j < 0 {
			return nil, cascade.Newf(cascade.KindIntegrity, "generate: compiled template %s has an unclosed value", src)
		}
		name := rest[i+2 : i+j]
		val, ok := vals[name]
		if !ok {
			return nil, cascade.Newf(cascade.KindIntegrity, "generate: compiled template %s names unknown value %q", src, name)
		}
		b.WriteString(rest[:i])
		b.WriteString(val)
		rest = rest[i+j+2:]
	}
}

// WriteAgentsSkills applies rs through w after checking every target: an
// existing target with a mangled marker is KindInvalidInput wrapping
// ErrMangledMarker, and a target directory that cannot take a file is
// KindPermissionDenied. Either way nothing is written, not even a sidecar.
// After the checks each file goes through Writer.Apply, so a hand edit is
// refused with a sidecar and survives.
func WriteAgentsSkills(ctx context.Context, w *Writer, rs []Rendered) ([]Outcome, error) {
	if w == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "generate: nil Writer")
	}
	probed := map[string]bool{}
	for _, r := range rs {
		if err := checkTarget(w.root, r.Path); err != nil {
			return nil, err
		}
		if err := probeWritable(w.root, pathpkg.Dir(r.Path), probed); err != nil {
			return nil, err
		}
	}
	out := make([]Outcome, 0, len(rs))
	for _, r := range rs {
		o, err := w.Apply(ctx, r)
		out = append(out, o)
		if err != nil {
			return out, err
		}
	}
	return out, nil
}

// checkTarget refuses an existing target whose markers are mangled.
func checkTarget(root, rel string) error {
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return cascade.Wrapf(cascade.KindUnavailable, err, "generate: read %s", rel)
	}
	if _, err := ParseManagedBlocks(data, FormMarkdown, nil); err != nil {
		return cascade.Wrapf(cascade.KindInvalidInput, err, "generate: %s holds a mangled managed-block marker; nothing was written", rel)
	}
	return nil
}

// probeWritable creates and removes a temporary file in the nearest
// existing directory of relDir under root, once per directory.
func probeWritable(root, relDir string, probed map[string]bool) error {
	dir := relDir
	for dir != "." && dir != "/" {
		if st, err := os.Stat(filepath.Join(root, filepath.FromSlash(dir))); err == nil && st.IsDir() {
			break
		}
		dir = pathpkg.Dir(dir)
	}
	if probed[dir] {
		return nil
	}
	f, err := os.CreateTemp(filepath.Join(root, filepath.FromSlash(dir)), ".cascade-probe-*")
	if err != nil {
		return cascade.Wrapf(cascade.KindPermissionDenied, err, "generate: %s is not writable; nothing was written", dir)
	}
	name := f.Name()
	cerr := f.Close()
	if rerr := os.Remove(name); rerr != nil || cerr != nil {
		return cascade.Wrapf(cascade.KindUnavailable, errors.Join(cerr, rerr), "generate: remove write probe in %s", dir)
	}
	probed[dir] = true
	return nil
}
