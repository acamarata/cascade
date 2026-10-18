// Purpose: targeted writers for a RuntimeProfile's endpoint and runtime
//
//	config. UpsertRuntimeProfile is unchanged (its conflict clause
//	overwrites every column); these UPDATE only the named columns, so an
//	endpoint or config home set here survives until it is set again.
//
// Inputs: a profile id plus the values to record. Outputs: nil or a
//
//	*cascade.Error (ErrTopologyInvariant, ErrTopologyNotFound,
//	KindUnavailable on a database failure -- never a silent no-op).
//
// Constraints: every value is validated before SQL; a refused write leaves
//
//	the row byte-identical; a config home must lie inside the profile's own
//	home (symlinks resolved), so one account's config can never point into
//	another's.
//
// SPORT: fleet/topology/profile_store/ADD (P1-TOP-01).

package topology

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

// envVarPattern is the shape of a portable environment-variable name; it
// excludes "=", whitespace and NUL, which would smuggle a second variable.
var envVarPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// SetEndpoint records ep as the network endpoint of a PERSISTENT profile.
// A non-persistent profile returns ErrTopologyInvariant and is not changed;
// an unknown id returns ErrTopologyNotFound. A zero ep clears the endpoint.
func (s *Store) SetEndpoint(ctx context.Context, id RuntimeProfileID, ep Endpoint) error {
	if err := validateEndpoint(id, ep); err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `UPDATE `+tableRuntimeProfile+
		` SET endpoint_host = ?, endpoint_port = ? WHERE id = ? AND persistent = 1`,
		ep.Host, ep.Port, string(id))
	if err != nil {
		return cascade.Wrapf(cascade.KindUnavailable, err, "topology: set endpoint %q", id)
	}
	return s.requireOneRow(ctx, res, id, true)
}

// validateEndpoint refuses an endpoint that is not a clean host/port pair:
// whitespace or NUL, a "unix:" host that URLIsLocal does not read as a
// socket, a network host holding ':' (a host:port string; only a bare IPv6
// literal may), a host that does not wrap as a URL authority (a path, query,
// fragment or userinfo; see endpointURL), or a port that does not match the
// host kind.
func validateEndpoint(id RuntimeProfileID, ep Endpoint) error {
	if strings.ContainsAny(ep.Host, " \t\r\n\x00") {
		return newInvariantErr("runtime_profile", string(id), "endpoint host holds whitespace or NUL")
	}
	isUnix := strings.HasPrefix(ep.Host, unixEndpointPrefix)
	if isUnix && !URLIsLocal(ep.Host) {
		return newInvariantErr("runtime_profile", string(id), "unix endpoint needs an absolute socket path")
	}
	if !isUnix && strings.Contains(ep.Host, ":") && !isBareIPv6(ep.Host) {
		return newInvariantErr("runtime_profile", string(id), "endpoint host holds a port or is not an IPv6 literal")
	}
	if _, ok := endpointURL(ep.Host); ep.Host != "" && !ok {
		return newInvariantErr("runtime_profile", string(id), "endpoint host carries a path, query, fragment or userinfo")
	}
	hasPort := ep.Port != 0
	noPortHost := ep.Host == "" || isUnix
	switch {
	case ep.Port < 0 || ep.Port > 65535:
		return newInvariantErr("runtime_profile", string(id), "endpoint port out of range")
	case noPortHost && hasPort:
		return newInvariantErr("runtime_profile", string(id), "an empty or unix endpoint carries no port")
	case !noPortHost && !hasPort:
		return newInvariantErr("runtime_profile", string(id), "a network endpoint needs a port")
	}
	return nil
}

// isBareIPv6 reports whether h has the shape of a bare IPv6 literal: at least
// two colons and only hex digits, ':' and '.' (an embedded IPv4 tail). It is
// a shape check, not a parse (no "net" import); EndpointIsLocal still
// decides locality by URLIsLocal and reads anything else remote.
func isBareIPv6(h string) bool {
	if strings.Count(h, ":") < 2 {
		return false
	}
	for _, r := range h {
		isHex := (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')
		if !isHex && r != ':' && r != '.' {
			return false
		}
	}
	return true
}

// SetRuntimeConfig records the profile's config-home environment variable
// and directory. Both must be non-empty; configHome (cleaned, existing
// parents resolved through EvalSymlinks) must lie inside HomePath(root, id),
// else ErrTopologyInvariant and the row is unchanged.
func (s *Store) SetRuntimeConfig(ctx context.Context, id RuntimeProfileID, envVar, configHome, root string) error {
	if envVar == "" || configHome == "" {
		return newInvariantErr("runtime_profile", string(id), "env var and config home are set together")
	}
	if !envVarPattern.MatchString(envVar) {
		return newInvariantErr("runtime_profile", string(id), "env var name is not portable")
	}
	home, err := HomePath(root, id)
	if err != nil {
		return err
	}
	if err := requireInside(id, home, configHome); err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `UPDATE `+tableRuntimeProfile+
		` SET env_var = ?, config_home = ? WHERE id = ?`, envVar, filepath.Clean(configHome), string(id))
	if err != nil {
		return cascade.Wrapf(cascade.KindUnavailable, err, "topology: set runtime config %q", id)
	}
	return s.requireOneRow(ctx, res, id, false)
}

// requireOneRow turns a zero-row UPDATE into the right typed error: an
// unknown id is ErrTopologyNotFound, a known non-persistent profile (when
// the write needed persistence) is ErrTopologyInvariant.
func (s *Store) requireOneRow(ctx context.Context, res sql.Result, id RuntimeProfileID, needPersistent bool) error {
	n, err := res.RowsAffected()
	if err != nil {
		return cascade.Wrapf(cascade.KindUnavailable, err, "topology: update runtime profile %q", id)
	}
	if n == 1 {
		return nil
	}
	rec, err := s.GetRuntimeProfile(ctx, id)
	if err != nil {
		return err
	}
	if needPersistent && !rec.Persistent {
		return newInvariantErr("runtime_profile", string(id), "profile is not persistent")
	}
	return cascade.Wrapf(cascade.KindConflict, ErrTopologyConflict, "runtime profile %q changed during update", id)
}

// requireInside refuses a configHome that is relative or resolves outside
// the profile home. The home itself must be a real directory (or absent): a
// symlink, junction or file AT the home path is refused, so a link can never
// make another account's home count as this one's. Only the home's parent is
// resolved; configHome is resolved through its deepest existing ancestor, so
// a symlink inside the home that points elsewhere is caught too.
func requireInside(id RuntimeProfileID, home, configHome string) error {
	if !filepath.IsAbs(configHome) {
		return newInvariantErr("runtime_profile", string(id), "config home must be an absolute path")
	}
	if err := requireRealDirOrAbsent(id, filepath.Dir(home)); err != nil {
		return err
	}
	if err := requireRealDirOrAbsent(id, home); err != nil {
		return err
	}
	realParent, err := resolveExisting(filepath.Dir(home))
	if err != nil {
		return newInvariantErr("runtime_profile", string(id), "cannot resolve profile home: "+err.Error())
	}
	realHome := filepath.Join(realParent, filepath.Base(home))
	realCfg, err := resolveExisting(configHome)
	if err != nil {
		return newInvariantErr("runtime_profile", string(id), "cannot resolve config home: "+err.Error())
	}
	rel, err := filepath.Rel(realHome, realCfg)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return newInvariantErr("runtime_profile", string(id), "config home is outside the profile home")
	}
	return nil
}

// requireRealDirOrAbsent refuses a home path that exists as anything but a
// real directory (symlink, junction/irregular, file); a missing path is fine.
// It guards both the home and its profiles/ parent (the unix EnsureHome
// leg checks the same two).
func requireRealDirOrAbsent(id RuntimeProfileID, home string) error {
	info, err := os.Lstat(home)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		return newInvariantErr("runtime_profile", string(id), "cannot inspect profile home: "+err.Error())
	case !info.IsDir() || info.Mode()&(fs.ModeSymlink|fs.ModeIrregular) != 0:
		return newInvariantErr("runtime_profile", string(id), "profile home is not a real directory")
	}
	return nil
}

// resolveExisting cleans p and resolves symlinks through its deepest
// existing ancestor, re-appending the not-yet-created tail. Only an entry
// that Lstat also reports missing is walked past; a dangling link and any
// other error are returned (never read as absence).
func resolveExisting(p string) (string, error) {
	p = filepath.Clean(p)
	var tail []string
	for cur := p; ; {
		resolved, err := filepath.EvalSymlinks(cur)
		if err == nil {
			for i := len(tail) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, tail[i])
			}
			return resolved, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		// A dangling link also reports "does not exist" from EvalSymlinks,
		// but the entry is there: it is not a directory still to be created.
		if _, lerr := os.Lstat(cur); !errors.Is(lerr, fs.ErrNotExist) {
			return "", newInvariantErr("runtime_profile", cur, "path holds a link that does not resolve")
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", err
		}
		tail = append(tail, filepath.Base(cur))
		cur = parent
	}
}
