package fixture

import (
	"context"
	"github.com/acamarata/cascade/internal/jobs"
)

func callers(r *jobs.RepoRegistry) {
	_, _ = r.Register(context.Background(), "", "", nil)
	_ = r.RecordTrustFetch(context.Background(), "", "origin", "url", nil)
	wireCiRepoRPC()
}
