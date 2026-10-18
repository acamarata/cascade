// Purpose: provisions the per-spawn runtime profile a driver child runs in:
// <base>/<harness>/<jobID>/{home,home/config,tmp}, every directory 0700 and
// owned by the daemon, with a subscription harness's vendor auth file
// linked into the config directory in place.
// Inputs: the profile base, the binding, the validated DriverAuth, the job id.
// Outputs: a RuntimeProfile, or an error with every created directory removed.
// Constraints: the auth file's content is never opened or copied (a symlink
// only); the source must be a regular file owned by the daemon uid, not
// group or world writable, in a directory only the daemon user can write,
// under ancestors that no other user can write (group-writable ancestors are
// accepted, world-writable ones only when sticky), else provider.ErrEntitlement; the job directory must not exist yet (one profile
// per spawn); the home is never the daemon HOME nor the vendor config
// directory the daemon env names. Unix only: on windows the modes carry no
// meaning, so the call is KindUnavailable.
// SPORT: internal.providers.agents/ADD (P1-AGT-15, contract:driver-credentials).

package agents

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// RuntimeProfile is the three directories one spawn runs in.
type RuntimeProfile struct {
	Home      string
	ConfigDir string
	Tmp       string
}

// jobIDName is the shape a job id must have to name a directory.
var jobIDName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// ProvisionRuntimeProfile creates the profile for one job under base.
func ProvisionRuntimeProfile(base string, b Binding, a DriverAuth, jobID string) (RuntimeProfile, error) {
	if runtime.GOOS == "windows" {
		return RuntimeProfile{}, cascade.New(cascade.KindUnavailable, "agents: runtime profiles are not supported on windows")
	}
	if err := checkProfileInputs(base, b, a, jobID); err != nil {
		return RuntimeProfile{}, err
	}
	var source checkedSource
	if a.Class == CredentialSubscription {
		var err error
		if source, err = checkAuthSource(a.AuthSource); err != nil {
			return RuntimeProfile{}, err
		}
	}
	harnessDir, err := ensureHarnessDir(base, b)
	if err != nil {
		return RuntimeProfile{}, err
	}
	jobDir := filepath.Join(harnessDir, jobID)
	if err := makePrivateDir(jobDir, true); err != nil {
		return RuntimeProfile{}, err
	}
	p, err := buildProfile(jobDir, b, a, source)
	if err != nil {
		_ = os.RemoveAll(jobDir) // removes a link itself, never what it points at
		return RuntimeProfile{}, err
	}
	return p, nil
}

// checkProfileInputs validates the names that become path components.
func checkProfileInputs(base string, b Binding, a DriverAuth, jobID string) error {
	switch {
	case !filepath.IsAbs(base):
		return cascade.New(cascade.KindInvalidInput, "agents: profile base must be an absolute path")
	case !jobIDName.MatchString(jobID):
		return cascade.New(cascade.KindInvalidInput, "agents: job id is not a bare directory name")
	case !jobIDName.MatchString(string(b.Harness)):
		return cascade.New(cascade.KindInvalidInput, "agents: harness id is not a bare directory name")
	case a.Class == CredentialSubscription && !filepath.IsAbs(a.AuthSource):
		return cascade.New(cascade.KindInvalidInput, "agents: auth source must be an absolute path")
	case a.Harness != b.Harness:
		return cascade.Newf(cascade.KindInvalidInput, "agents: credentials for %q cannot provision a %q profile", a.Harness, b.Harness)
	}
	_, err := KeyVarFor(b, a.Class)
	return err
}

// ensureHarnessDir creates base and base/<harness> when missing and requires
// both to be private directories owned by the daemon.
func ensureHarnessDir(base string, b Binding) (string, error) {
	if err := os.MkdirAll(base, 0o700); err != nil {
		return "", cascade.Wrap(cascade.KindUnavailable, err, "agents: profile base could not be created")
	}
	if err := verifyPrivateDir(base); err != nil {
		return "", err
	}
	dir := filepath.Join(base, string(b.Harness))
	return dir, makePrivateDir(dir, false)
}

// buildProfile creates home, home/config and tmp inside jobDir and links the
// subscription auth file.
func buildProfile(jobDir string, b Binding, a DriverAuth, source checkedSource) (RuntimeProfile, error) {
	p := RuntimeProfile{Home: filepath.Join(jobDir, "home"), Tmp: filepath.Join(jobDir, "tmp")}
	p.ConfigDir = filepath.Join(p.Home, "config")
	for _, dir := range []string{p.Home, p.ConfigDir, p.Tmp} {
		if err := makePrivateDir(dir, true); err != nil {
			return RuntimeProfile{}, err
		}
	}
	if err := refuseSharedHome(p, b); err != nil {
		return RuntimeProfile{}, err
	}
	if a.Class == CredentialSubscription {
		if err := linkAuthSource(p.ConfigDir, source); err != nil {
			return RuntimeProfile{}, err
		}
	}
	return p, nil
}

// makePrivateDir creates dir with mode 0700 and verifies it. fresh demands
// that dir did not exist; otherwise an existing private directory is reused.
func makePrivateDir(dir string, fresh bool) error {
	err := os.Mkdir(dir, 0o700)
	switch {
	case err == nil:
	case os.IsExist(err) && !fresh:
	case os.IsExist(err):
		return cascade.Newf(cascade.KindConflict, "agents: profile directory %s already exists", dir)
	default:
		return cascade.Wrapf(cascade.KindUnavailable, err, "agents: profile directory %s could not be created", dir)
	}
	return verifyPrivateDir(dir)
}

// verifyPrivateDir requires a real directory (not a link), owned by the
// daemon, that no group or other user can access.
func verifyPrivateDir(dir string) error {
	fi, err := os.Lstat(dir)
	if err != nil {
		return cascade.Wrapf(cascade.KindUnavailable, err, "agents: profile directory %s could not be inspected", dir)
	}
	if !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 {
		return cascade.Newf(cascade.KindPermissionDenied, "agents: profile path %s is not a real directory", dir)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return cascade.Newf(cascade.KindPermissionDenied, "agents: profile directory %s has mode %v, want 0700", dir, fi.Mode().Perm())
	}
	if !ownedByDaemon(fi) {
		return cascade.Newf(cascade.KindPermissionDenied, "agents: profile directory %s is not owned by the daemon user", dir)
	}
	return nil
}

// refuseSharedHome refuses a profile whose home is the daemon's HOME or the
// vendor config directory the daemon environment names.
func refuseSharedHome(p RuntimeProfile, b Binding) error {
	home := resolved(p.Home)
	for _, shared := range []string{os.Getenv("HOME"), os.Getenv(b.ConfigDirVar)} {
		if shared != "" && (resolved(shared) == home || resolved(shared) == resolved(p.ConfigDir)) {
			return cascade.Newf(cascade.KindPermissionDenied, "agents: profile home %s is the daemon's own home or vendor config directory", p.Home)
		}
	}
	return nil
}

// resolved is path with symlinks evaluated, or cleaned when it cannot be.
func resolved(path string) string {
	if r, err := filepath.EvalSymlinks(path); err == nil {
		return r
	}
	return filepath.Clean(path)
}

// checkedSource is a vendor auth file that passed every check: its info at
// check time and its path with the directory symlinks resolved, which is the
// path the profile links to.
type checkedSource struct {
	info os.FileInfo
	path string
}

// authFileOK reports whether fi is a regular file owned by the daemon user
// that no group or other user can write.
func authFileOK(fi os.FileInfo) bool {
	return fi.Mode().IsRegular() && fi.Mode().Perm()&0o022 == 0 && ownedByDaemon(fi)
}

// checkAuthSource lstats the vendor auth file, resolves its directory and
// walks every directory above it to the root without opening anything. The
// file itself may not be a link; its directory must be writable by the daemon
// user alone; each ancestor must belong to the daemon user or root and may not
// be world-writable (a sticky one such as /tmp is allowed: another user cannot
// rename entries in it). A group-writable ancestor is accepted: the group is
// the daemon user's own trust domain (macOS staff, a shared volume root).
func checkAuthSource(path string) (checkedSource, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return checkedSource{}, cascade.Wrapf(cascade.KindPolicyDenied, provider.ErrEntitlement, "agents: auth source is not usable (%v)", err)
	}
	if !authFileOK(fi) {
		return checkedSource{}, cascade.Wrap(cascade.KindPolicyDenied, provider.ErrEntitlement,
			"agents: auth source must be a regular file owned by the daemon user and not group or world writable")
	}
	dir, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		return checkedSource{}, cascade.Wrapf(cascade.KindPolicyDenied, provider.ErrEntitlement, "agents: auth source directory is not usable (%v)", err)
	}
	for d, first := dir, true; ; d = filepath.Dir(d) {
		di, err := os.Lstat(d)
		if why := dirProblem(di, err, first); why != "" {
			return checkedSource{}, cascade.Wrapf(cascade.KindPolicyDenied, provider.ErrEntitlement, "agents: auth source %s (%s)", why, d)
		}
		if first = false; d == filepath.Dir(d) {
			break
		}
	}
	return checkedSource{info: fi, path: filepath.Join(dir, filepath.Base(path))}, nil
}

// dirProblem names what is wrong with one directory on the auth source's
// path, or "" when it is fine. The file's own directory (own) may not be
// group or world writable; a higher one may not be world writable unless it
// is sticky, and a group-writable ancestor is accepted (the group is the
// daemon user's own trust domain, such as macOS staff or a shared volume root).
func dirProblem(di os.FileInfo, err error, own bool) string {
	switch {
	case err != nil || !di.IsDir():
		return "directory is not a usable directory"
	case !ownedByDaemonOrRoot(di):
		return "directory or an ancestor belongs to neither the daemon user nor root"
	case own && di.Mode().Perm()&0o022 != 0:
		return "directory must be writable by the daemon user alone"
	case !own && di.Mode().Perm()&0o002 != 0 && di.Mode()&os.ModeSticky == 0:
		return "directory has an ancestor that can be written by another user"
	}
	return ""
}

// linkAuthSource symlinks the checked source into configDir under its own
// basename and confirms the file is still the one checked and still safe, so
// a swap or a loosened mode between the check and the link is refused. The
// content is never opened.
func linkAuthSource(configDir string, src checkedSource) error {
	link := filepath.Join(configDir, filepath.Base(src.path))
	if err := os.Symlink(src.path, link); err != nil {
		return cascade.Wrapf(cascade.KindUnavailable, err, "agents: auth source could not be linked")
	}
	now, err := os.Lstat(src.path)
	if err != nil || !os.SameFile(src.info, now) || !authFileOK(now) {
		return cascade.Wrap(cascade.KindPolicyDenied, provider.ErrEntitlement, "agents: auth source changed while it was being linked")
	}
	return nil
}

// ownedByDaemon reports whether fi belongs to the daemon's effective uid.
func ownedByDaemon(fi os.FileInfo) bool {
	uid, ok := ownerUID(fi)
	return ok && uid == os.Geteuid()
}

// ownedByDaemonOrRoot also accepts root, who can write anywhere anyway.
func ownedByDaemonOrRoot(fi os.FileInfo) bool {
	uid, ok := ownerUID(fi)
	return ok && (uid == os.Geteuid() || uid == 0)
}

// ownerUID reads the owning uid from fi.Sys() by field name, so this file
// builds on every platform; ok is false where the platform has no uid.
func ownerUID(fi os.FileInfo) (int, bool) {
	v := reflect.ValueOf(fi.Sys())
	if v.Kind() == reflect.Pointer {
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return 0, false
	}
	f := v.FieldByName("Uid")
	if !f.IsValid() || !f.CanUint() {
		return 0, false
	}
	return int(f.Uint()), true
}
