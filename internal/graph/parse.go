package graph

import (
	"bytes"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// ParseFile reads and unmarshals a graph declaration from a YAML file.
// It does not validate semantics; call Validate for that.
func ParseFile(path string) (*Graph, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read graph file: %w", err)
	}
	return Parse(data)
}

// Parse unmarshals a graph declaration from YAML bytes.
func Parse(data []byte) (*Graph, error) {
	var g Graph
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&g); err != nil {
		return nil, fmt.Errorf("parse graph yaml: %w", err)
	}
	return &g, nil
}
