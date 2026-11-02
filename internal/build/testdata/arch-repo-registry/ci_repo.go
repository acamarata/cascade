package fixture

import (
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/pkg/envelope"
	"github.com/spf13/cobra"
)

func command(r *jobs.RepoRegistry, s envelope.Signer) *cobra.Command {
	return &cobra.Command{RunE: func(*cobra.Command, []string) error {
		_, err := r.WithTrustRecordSigner(s)
		fixtureHelper()
		return err
	}}
}
