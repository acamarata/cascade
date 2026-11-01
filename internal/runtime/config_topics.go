package runtime

// Purpose: the [topics] config surface (P1-CPA-12): which conductor lane
//   classifies chat turns for topic auto-filing, and on which model.
// Inputs: the decoded generic config tree (Config.Extra["topics"]).
// Outputs: TopicsSettings, or a *ConfigError naming the bad key.
// Constraints: the table is strict. Its keys are closed (classifier_lane,
//   classifier_model), each must be a string, and a lane requires a model.
//   An absent table, or an empty classifier_lane, means auto-filing is
//   off: no turn is ever sent to a classifier.
// SPORT: runtime/config.topics (ADD, P1-CPA-12).

// TopicsSettings is the [topics] table.
type TopicsSettings struct {
	// ClassifierLane names the conductor lane that classifies turns; ""
	// turns topic auto-filing off.
	ClassifierLane string
	// ClassifierModel is the model the classifier lane runs. It is
	// required when ClassifierLane is set.
	ClassifierModel string
}

// Enabled reports whether topic auto-filing has a classifier lane.
func (s TopicsSettings) Enabled() bool { return s.ClassifierLane != "" }

// TopicsSettingsFrom reads [topics] from cfg strictly. A nil cfg or an
// absent table is the zero value (auto-filing off) with no error.
func TopicsSettingsFrom(cfg *Config) (TopicsSettings, error) {
	if cfg == nil || cfg.Extra == nil {
		return TopicsSettings{}, nil
	}
	raw, present := cfg.Extra["topics"]
	if !present || raw == nil {
		return TopicsSettings{}, nil
	}
	table, ok := raw.(map[string]interface{})
	if !ok {
		return TopicsSettings{}, &ConfigError{Field: "topics", Reason: "must be a table"}
	}
	var out TopicsSettings
	for key, value := range table {
		s, err := topicsString(key, value)
		if err != nil {
			return TopicsSettings{}, err
		}
		switch key {
		case "classifier_lane":
			out.ClassifierLane = s
		case "classifier_model":
			out.ClassifierModel = s
		}
	}
	if out.ClassifierLane != "" && out.ClassifierModel == "" {
		return TopicsSettings{}, &ConfigError{Field: "topics.classifier_model",
			Reason: "a classifier_lane requires a classifier_model"}
	}
	return out, nil
}

// topicsString checks one [topics] key name and reads its string value.
func topicsString(key string, value interface{}) (string, error) {
	switch key {
	case "classifier_lane", "classifier_model":
	default:
		return "", &ConfigError{Field: "topics." + key, Reason: "unrecognised key in [topics]"}
	}
	s, ok := value.(string)
	if !ok {
		return "", &ConfigError{Field: "topics." + key, Reason: "must be a string"}
	}
	return s, nil
}
