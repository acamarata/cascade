// Purpose: the enrolled dispatch route of a node and its fail-closed
//
//	resolution (P1-NODE-18).
//
// Inputs: the enrollment payload (user@host:port, socket_dir) and the
//
//	host-key fingerprint EnrollNode has already verified.
//
// Outputs: DeviceRecord.NodeRoute, and RecordStore.Target, which resolves
//
//	a node's route or refuses with a typed error.
//
// Constraints: a route is never guessed. A missing or incomplete route is
//
//	KindNotFound; a revoked or drained node is KindPermissionDenied.
//	DeviceRecord.Route already names travel.go's prober fallback
//	(*RouteConfig, JSON "route"), so the enrolled route lives in the
//	additive NodeRoute field (JSON "node_route") instead.

package nodes

import (
	"path"
	"path/filepath"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

// maxSocketDirBytes bounds the socket_dir payload field.
const maxSocketDirBytes = 256

// Route is the enrolled dispatch route of a node: the ssh user and
// host:port it was enrolled over, the host-key fingerprint EnrollNode
// verified, and the node's absolute <dataDir>/nodes socket directory.
type Route struct {
	User      string `json:"user,omitempty"`
	Addr      string `json:"addr,omitempty"`
	HostKeyFP string `json:"host_key_fp,omitempty"`
	SocketDir string `json:"socket_dir,omitempty"`
}

// complete reports whether every route field is set.
func (r Route) complete() bool {
	return r.User != "" && r.Addr != "" && r.HostKeyFP != "" && r.SocketDir != ""
}

// validateSocketDir refuses a socket_dir that is relative, longer than
// maxSocketDirBytes or holds a NUL byte. Empty is allowed: the field is
// optional and an absent value leaves the route incomplete.
func validateSocketDir(dir string) error {
	switch {
	case dir == "":
		return nil
	case strings.IndexByte(dir, 0) >= 0:
		return cascade.New(cascade.KindInvalidInput, "nodes: enroll payload socket_dir holds a NUL byte")
	case len(dir) > maxSocketDirBytes:
		return cascade.Newf(cascade.KindInvalidInput, "nodes: enroll payload socket_dir exceeds %d bytes", maxSocketDirBytes)
	case !path.IsAbs(dir) && !filepath.IsAbs(dir):
		return cascade.New(cascade.KindInvalidInput, "nodes: enroll payload socket_dir is not an absolute path")
	}
	return nil
}

// routeFromPayload builds the route EnrollNode records. p.Host is
// user@host:port; a host without a user part leaves User empty, so the
// route stays incomplete rather than defaulted. p.HostKeyFingerprint is
// only used after KnownHosts.Verify accepted it.
func routeFromPayload(p EnrollPayload) Route {
	r := Route{HostKeyFP: p.HostKeyFingerprint, SocketDir: p.SocketDir, Addr: p.Host}
	if i := strings.LastIndexByte(p.Host, '@'); i >= 0 {
		r.User, r.Addr = p.Host[:i], p.Host[i+1:]
	}
	return r
}

// Target resolves nodeID's enrolled route into a dial Target. A node with
// no record, or with a route missing any field, is KindNotFound: the
// route is never guessed. A revoked or drained node is
// KindPermissionDenied.
func (s *RecordStore) Target(nodeID string) (Target, Route, error) {
	rec, err := s.Get(nodeID)
	if err != nil {
		return Target{}, Route{}, err
	}
	if SignatureRevoked(rec, rec.PubKeyB64) {
		return Target{}, Route{}, cascade.Newf(cascade.KindPermissionDenied, "nodes: node %q is revoked; no route is resolved", nodeID)
	}
	if rec.Drained {
		return Target{}, Route{}, cascade.Newf(cascade.KindPermissionDenied, "nodes: node %q is drained; no route is resolved", nodeID)
	}
	if !rec.NodeRoute.complete() {
		return Target{}, Route{}, cascade.Newf(cascade.KindNotFound, "nodes: node %q has no complete enrolled route", nodeID)
	}
	r := rec.NodeRoute
	return Target{NodeID: rec.NodeID, User: r.User, Addr: r.Addr}, r, nil
}
