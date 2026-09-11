// Purpose: error helpers every driver in this package shares — context
// classification and the not-found/absent-binary refusals — so fs.go,
// s3.go and rclone.go do not each reinvent the same small mapping.
//
// SPORT: internal.backup.targets/ADDED (P1-E19-W4-S41-T3).

package targets

import (
	"context"

	"github.com/acamarata/cascade/pkg/cascade"
)

// ctxErr classifies a context's own Err() into the taxonomy: a canceled
// context is KindCanceled, a deadline is KindTimeout. Callers check
// ctx.Err() before issuing any I/O so a caller-canceled operation never
// reaches the real filesystem/network/process.
func ctxErr(err error) error {
	if err == context.Canceled {
		return cascade.Wrap(cascade.KindCanceled, err, "targets: context canceled")
	}
	return cascade.Wrap(cascade.KindTimeout, err, "targets: context deadline exceeded")
}
