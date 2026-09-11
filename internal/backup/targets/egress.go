// Purpose: the backup-target egress wiring (R-21.265): every remote
// target (s3, rclone) obtains its unforgeable egress.Capability through
// this file before it may write or list a single byte, and transits
// Intercept(ctx, EgressClassBackupTarget, tier, content) with the tier
// (TierRestricted — a backup of restricted material is the point of a
// backup, per classes.go's AllowRestricted note) passed explicitly
// (R-21.228). fs.go never calls this: a local filesystem write is not
// egress.
//
// CONTRACT-VS-TREE: this ticket's task text says to "call RegisterClass
// from internal/backup/targets/egress.go package init" and to "append
// EgressClassBackupTarget to internal/hooks/egress/classes.go". Neither
// action is taken here: grepping the egress package finds no exported
// "RegisterClass" function at all (only *Registry.Register/MustRegister,
// methods, never a package-level func by that name — matching the exact
// gap internal/plugins/registryfetch/fetch.go already recorded for an
// earlier ticket against this same not-yet-real API), and
// EgressClassBackupTarget is ALREADY present in classes.go's
// defaultClasses table with Owner "S/S-41.T3" and AllowRestricted: true —
// registered before this ticket ran, not by it. Re-registering it here
// via MustRegister would panic (ErrDuplicateClass). This file therefore
// only ACQUIRES the capability the existing registration already grants,
// via the real API: engine.Capability(EgressClassBackupTarget).
//
// SPORT: internal.backup.targets.egress/ADDED (P1-E19-W4-S41-T3).

package targets

import (
	"context"

	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/pkg/cascade"
)

// backupTargetTier is the sensitivity tier every remote target declares
// for its outbound payload. The bytes are always S-41.T1's pre-encrypted
// opaque objects, but the class's AllowRestricted bit is what admits
// them regardless — TierRestricted is the honest declaration of what
// this content actually is before that encryption is accounted for.
const backupTargetTier = egress.TierRestricted

// acquireBackupTargetCapability obtains the unforgeable Capability for
// EgressClassBackupTarget from engine, refusing (never panicking) if the
// class is missing or disabled in the engine's registry.
func acquireBackupTargetCapability(engine *egress.Engine) (egress.Capability, error) {
	if engine == nil {
		return egress.Capability{}, cascade.New(cascade.KindUnavailable,
			"targets: no egress engine configured; a remote target cannot write without one")
	}
	token, err := engine.Capability(egress.EgressClassBackupTarget)
	if err != nil {
		return egress.Capability{}, cascade.Wrap(cascade.KindPolicyDenied, err,
			"targets: acquiring the backup-target egress capability")
	}
	return token, nil
}

// interceptOutbound is the one call site every remote target's Put (and
// any other outbound-payload path) transits before a byte leaves the
// process, per R-21.265/R-21.228.
func interceptOutbound(ctx context.Context, engine *egress.Engine, token egress.Capability, content []byte) ([]byte, error) {
	out, err := engine.Intercept(ctx, token, backupTargetTier, content)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindPolicyDenied, err, "targets: egress intercept refused the outbound payload")
	}
	return out, nil
}
