package evidence

import (
	"encoding/json"

	"github.com/acamarata/cascade/pkg/provider"
)

// Purpose: retain stored vocabulary in Claim and Evidence JSON.
// Inputs: typed records or JSON objects.
// Outputs: records with optional canonical tiers and unchanged other fields.
// Constraints: absent and empty classes remain unset; unknown words refuse.
// SPORT: evidence data-class serialization.

// MarshalJSON preserves the stored class word, including the unset empty word.
func (c Claim) MarshalJSON() ([]byte, error) {
	type shadow Claim
	word := ""
	if c.DataClass != nil {
		var err error
		word, err = provider.DataClassName(*c.DataClass)
		if err != nil {
			return nil, err
		}
	}
	return json.Marshal(struct {
		shadow
		DataClass string `json:"data_class"`
	}{shadow(c), word})
}

// UnmarshalJSON validates the stored vocabulary without accepting tier wire names.
func (c *Claim) UnmarshalJSON(data []byte) error {
	type shadow Claim
	var wire struct {
		shadow
		DataClass string `json:"data_class"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	if wire.DataClass != "" {
		tier, err := provider.ParseDataClassName(wire.DataClass)
		if err != nil {
			return err
		}
		wire.shadow.DataClass = new(tier)
	}
	*c = Claim(wire.shadow)
	return nil
}

// MarshalJSON preserves the evidence class's stored word.
func (e Evidence) MarshalJSON() ([]byte, error) {
	type shadow Evidence
	word := ""
	if e.DataClass != nil {
		var err error
		word, err = provider.DataClassName(*e.DataClass)
		if err != nil {
			return nil, err
		}
	}
	return json.Marshal(struct {
		shadow
		DataClass string `json:"data_class"`
	}{shadow(e), word})
}

// UnmarshalJSON accepts only stored vocabulary or an unset class.
func (e *Evidence) UnmarshalJSON(data []byte) error {
	type shadow Evidence
	var wire struct {
		shadow
		DataClass string `json:"data_class"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	if wire.DataClass != "" {
		tier, err := provider.ParseDataClassName(wire.DataClass)
		if err != nil {
			return err
		}
		wire.shadow.DataClass = new(tier)
	}
	*e = Evidence(wire.shadow)
	return nil
}
