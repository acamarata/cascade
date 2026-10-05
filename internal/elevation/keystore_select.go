// Purpose: rank sources and enroll without silent fallback.
// Inputs: registered sources. Outputs: custody or refusal.
// Constraints: source tier is authoritative. SPORT: elevation selection.

package elevation

import "runtime"

// Selector ranks explicitly registered custody sources.
type Selector struct {
	DataDir string
	Sources []CustodySource
}

func custodyRank(t CustodyTier) int {
	switch t {
	case CustodyPlatform:
		return 3
	case CustodyPresence:
		return 2
	case CustodyFile:
		return 1
	case CustodyNone:
		return 0
	default:
		return 0
	}
}

// Select returns the highest-ranked usable source.
func (s Selector) Select() Custody {
	best := Custody{tier: CustodyNone, storage: TierUnavailable, reason: "no protected source available", dataDir: s.DataDir}
	for _, source := range s.Sources {
		if source.Open == nil || custodyRank(source.Tier) <= custodyRank(best.Tier()) {
			continue
		}
		ks, ok := source.Open(s.DataDir)
		if !ok || (ks == nil && source.Tier != CustodyFile) {
			continue
		}
		best = Custody{tier: source.Tier, source: source.Name, storage: TierFile, ks: ks, dataDir: s.DataDir}
		if ks != nil {
			best.storage = ks.Tier()
		}
		if !source.Tier.SatisfiesElevation() {
			best.reason = "file possession does not prove local presence"
		}
	}
	return best
}

// Enroll generates a key without falling back after a source failure.
func (s Selector) Enroll() (Custody, error) {
	c := s.Select()
	ks, err := c.Signer()
	if err != nil {
		return c, err
	}
	if err := ks.GenerateKey(); err != nil {
		return c, err
	}
	return c, nil
}

// SelectCustody composes the production source registry.
func SelectCustody(dataDir string) Custody {
	c := (Selector{DataDir: dataDir, Sources: DefaultSources()}).Select()
	if runtime.GOOS == "windows" {
		c.reason = "windows-tier2"
	}
	return c
}

// Enroll generates a key without falling back after a source failure.
func Enroll(dataDir string) (Custody, error) {
	return (Selector{DataDir: dataDir, Sources: DefaultSources()}).Enroll()
}
