package runtime

import "testing"

// Purpose: config_topics.go's tests: the default-off table, the lane rule
//   and the closed key set, each through a real Load of a config.toml
//   under t.TempDir().

// topicsFrom loads body and reads [topics] from it.
func topicsFrom(t *testing.T, body string) (TopicsSettings, error) {
	t.Helper()
	cfg, err := loadRetrieval(t, body, nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return TopicsSettingsFrom(cfg)
}

func TestTopicsSettingsDefaultOff(t *testing.T) {
	for name, body := range map[string]string{
		"no file content": "",
		"no topics table": "[runtime]\nprofile = \"local\"\n",
		"empty table":     "[topics]\n",
		"empty lane":      "[topics]\nclassifier_lane = \"\"\n",
	} {
		t.Run(name, func(t *testing.T) {
			s, err := topicsFrom(t, body)
			if err != nil {
				t.Fatalf("TopicsSettingsFrom: %v", err)
			}
			if s != (TopicsSettings{}) || s.Enabled() {
				t.Fatalf("settings = %+v (enabled %v), want the zero value with auto-filing off", s, s.Enabled())
			}
		})
	}
	if s, err := TopicsSettingsFrom(nil); err != nil || s.Enabled() {
		t.Fatalf("TopicsSettingsFrom(nil) = %+v, %v, want off and no error", s, err)
	}
}

func TestTopicsClassifierLaneRequiresModel(t *testing.T) {
	_, err := topicsFrom(t, "[topics]\nclassifier_lane = \"topics-classifier\"\n")
	wantConfigField(t, err, "topics.classifier_model")
	if got, want := err.Error(), "runtime: config: topics.classifier_model: a classifier_lane requires a classifier_model"; got != want {
		t.Fatalf("err = %q, want %q", got, want)
	}

	s, err := topicsFrom(t, "[topics]\nclassifier_lane = \"topics-classifier\"\nclassifier_model = \"gemini-2.5-flash\"\n")
	if err != nil {
		t.Fatalf("lane with model: %v", err)
	}
	want := TopicsSettings{ClassifierLane: "topics-classifier", ClassifierModel: "gemini-2.5-flash"}
	if s != want || !s.Enabled() {
		t.Fatalf("settings = %+v, want %+v enabled", s, want)
	}
}

func TestTopicsSettingsStrictKeys(t *testing.T) {
	_, err := topicsFrom(t, "[topics]\nclassifier_lane = \"l\"\nclassifier_model = \"m\"\nthreshold = \"0.5\"\n")
	wantConfigField(t, err, "topics.threshold")

	_, err = topicsFrom(t, "[topics]\nclassifier_lane = 3\n")
	wantConfigField(t, err, "topics.classifier_lane")

	_, err = TopicsSettingsFrom(&Config{Extra: map[string]interface{}{"topics": "on"}})
	wantConfigField(t, err, "topics")
}
