package graph

import (
	"fmt"
	"strings"
)

// Ref is a parsed cross-node reference of the form
// "${<nodeID>.output.<field>[.<field>...]}".
type Ref struct {
	NodeID string
	Path   []string // fields under the node's output
}

// ParseRef parses a "${node.output.a.b}" reference. It returns ok=false if s
// is not a wrapped ${...} reference at all; it returns an error if it is a
// reference but malformed.
func ParseRef(s string) (Ref, bool, error) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "${") || !strings.HasSuffix(s, "}") {
		return Ref{}, false, nil
	}
	inner := strings.TrimSuffix(strings.TrimPrefix(s, "${"), "}")
	parts := strings.Split(inner, ".")
	if len(parts) < 3 || parts[1] != "output" {
		return Ref{}, true, fmt.Errorf("bad reference %q: expected ${node.output.field...}", s)
	}
	if parts[0] == "" {
		return Ref{}, true, fmt.Errorf("bad reference %q: empty node id", s)
	}
	return Ref{NodeID: parts[0], Path: parts[2:]}, true, nil
}

// IsItemPlaceholder reports whether s is the ${item} or ${index} instance
// placeholder used inside a scatter node's Input.
func IsItemPlaceholder(s string) bool {
	t := strings.TrimSpace(s)
	return t == "${item}" || t == "${index}"
}
