package mod

import (
	"errors"
	"io/fs"
	"strings"
)

// ErrRuleMissing is returned when a requested rule file cannot be found.
type ErrRuleMissing string

func (e ErrRuleMissing) Error() string { return string(e) }

// ErrRuleEmpty is returned when a rule file contains no content.
var ErrRuleEmpty = errors.New("rule content is empty")

// ErrRuleName is returned when a rule name is invalid.
var ErrRuleName = errors.New("invalid rule name")

// LoadRules reads the specified rule files from the given fs.FS and returns
// a map of rule names to their content.
func LoadRules(fsys fs.FS, names []string) (map[string]string, error) {
	rules := make(map[string]string)
	for _, name := range names {
		if strings.Contains(name, "..") || strings.HasPrefix(name, "/") {
			return nil, ErrRuleName
		}
		data, err := fs.ReadFile(fsys, name)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil, ErrRuleMissing(name)
			}
			return nil, err
		}
		if len(data) == 0 {
			return nil, ErrRuleEmpty
		}
		rules[name] = string(data)
	}
	return rules, nil
}
