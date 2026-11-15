// Package conductpolicy defines abstract processing lanes and work kinds for policy enforcement.
package conductpolicy

// Lane represents a processing lane.
type Lane string

const (
	// LaneBulk is the bulk processing lane.
	LaneBulk Lane = "bulk"
	// LanePremium is the premium processing lane.
	LanePremium Lane = "premium"
)

// Valid reports whether the Lane is a supported value.
func (l Lane) Valid() bool {
	switch l {
	case LaneBulk, LanePremium:
		return true
	default:
		return false
	}
}

// WorkKind represents a kind of work.
type WorkKind string

const (
	// WorkLaunch is the launch work kind.
	WorkLaunch WorkKind = "launch"
	// WorkManage is the manage work kind.
	WorkManage WorkKind = "manage"
	// WorkBulk is the bulk work kind.
	WorkBulk WorkKind = "bulk"
)

// Valid reports whether the WorkKind is a supported value.
func (k WorkKind) Valid() bool {
	switch k {
	case WorkLaunch, WorkManage, WorkBulk:
		return true
	default:
		return false
	}
}
