package yamlstrict

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// CheckDuplicateKeys parses YAML data and returns an error if any mapping
// contains duplicate scalar keys. It walks all mappings at every depth,
// including sequences and alias targets, tracking visited nodes to avoid
// infinite loops.
func CheckDuplicateKeys(data []byte) error {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidYAMLInput, err)
	}
	visited := make(map[*yaml.Node]bool)
	return walkNode(&doc, visited)
}

func walkNode(n *yaml.Node, visited map[*yaml.Node]bool) error {
	if n == nil || visited[n] {
		return nil
	}
	visited[n] = true

	switch n.Kind {
	case yaml.DocumentNode:
		for _, child := range n.Content {
			if err := walkNode(child, visited); err != nil {
				return err
			}
		}
	case yaml.MappingNode:
		seen := make(map[string]bool)
		for i := 0; i < len(n.Content)-1; i += 2 {
			key := n.Content[i]
			if key.Kind != yaml.ScalarNode {
				return ErrInvalidYAMLInput
			}
			if seen[key.Value] {
				return fmt.Errorf("%w: key %q at line %d", ErrDuplicateKey, key.Value, key.Line)
			}
			seen[key.Value] = true
			if err := walkNode(key, visited); err != nil {
				return err
			}
			if err := walkNode(n.Content[i+1], visited); err != nil {
				return err
			}
		}
	case yaml.SequenceNode:
		for _, child := range n.Content {
			if err := walkNode(child, visited); err != nil {
				return err
			}
		}
	case yaml.AliasNode:
		if n.Alias != nil {
			if err := walkNode(n.Alias, visited); err != nil {
				return err
			}
		}
	}
	return nil
}
