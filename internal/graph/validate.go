package graph

import (
	"fmt"
	"strings"
	"time"
)

// Validate performs all static checks that must pass before a run starts:
// unique/well-formed ids, valid types, type-specific required fields,
// dependency existence, acyclicity, well-formed scatter references, and the
// v1 "no nested scatter" rule. It returns the first error encountered, with a
// stable ordering (structural errors before graph-shape errors).
func Validate(g *Graph) error {
	if g == nil {
		return fmt.Errorf("nil graph")
	}
	if len(g.Nodes) == 0 {
		return fmt.Errorf("graph has no nodes")
	}

	byID := make(map[string]*Node, len(g.Nodes))
	for _, n := range g.Nodes {
		if strings.TrimSpace(n.ID) == "" {
			return fmt.Errorf("node with empty id")
		}
		if strings.ContainsAny(n.ID, "[]") {
			return fmt.Errorf("node %q: id must not contain '[' or ']' (reserved for scatter instances)", n.ID)
		}
		if _, dup := byID[n.ID]; dup {
			return fmt.Errorf("duplicate node id %q", n.ID)
		}
		byID[n.ID] = n
	}

	for _, n := range g.Nodes {
		if err := validateNode(n, byID); err != nil {
			return err
		}
	}

	if err := checkAcyclic(g, byID); err != nil {
		return err
	}
	return nil
}

func validateNode(n *Node, byID map[string]*Node) error {
	switch n.Type {
	case NodeAgent, NodeCommand, NodeHuman:
	default:
		return fmt.Errorf("node %q: unknown type %q (want agent|command|human)", n.ID, n.Type)
	}

	// dependency existence
	for _, d := range n.Deps {
		if _, ok := byID[d]; !ok {
			return fmt.Errorf("node %q: dependency %q does not exist", n.ID, d)
		}
		if d == n.ID {
			return fmt.Errorf("node %q depends on itself", n.ID)
		}
	}
	// optional_inputs must be a subset of deps
	for _, o := range n.OptionalInputs {
		if !contains(n.Deps, o) {
			return fmt.Errorf("node %q: optional_input %q is not in deps", n.ID, o)
		}
	}

	// type-specific required fields
	switch n.Type {
	case NodeCommand:
		if len(n.Command) == 0 {
			return fmt.Errorf("node %q: command node requires a non-empty command", n.ID)
		}
		if n.Agent != "" {
			return fmt.Errorf("node %q: agent: applies to agent nodes only", n.ID)
		}
	case NodeAgent:
		if strings.TrimSpace(n.Prompt) == "" {
			return fmt.Errorf("node %q: agent node requires a prompt", n.ID)
		}
	case NodeHuman:
		if n.Agent != "" {
			return fmt.Errorf("node %q: agent: applies to agent nodes only", n.ID)
		}
		if strings.TrimSpace(n.Prompt) == "" {
			return fmt.Errorf("node %q: human node requires a prompt", n.ID)
		}
	}

	if n.Retry != nil {
		if n.Retry.Max < 0 {
			return fmt.Errorf("node %q: retry.max must be >= 0", n.ID)
		}
		if err := checkDuration(n.Retry.Backoff); err != nil {
			return fmt.Errorf("node %q: retry.backoff: %w", n.ID, err)
		}
	}
	if err := checkDuration(n.Timeout); err != nil {
		return fmt.Errorf("node %q: timeout: %w", n.ID, err)
	}

	// scatter checks
	if n.IsScatter() {
		if err := validateScatter(n, byID); err != nil {
			return err
		}
	}
	return nil
}

func validateScatter(n *Node, byID map[string]*Node) error {
	ref, ok, err := ParseRef(n.Foreach)
	if err != nil {
		return fmt.Errorf("node %q: foreach: %w", n.ID, err)
	}
	if !ok {
		return fmt.Errorf("node %q: foreach must be a ${node.output.field} reference, got %q", n.ID, n.Foreach)
	}
	src, ok := byID[ref.NodeID]
	if !ok {
		return fmt.Errorf("node %q: foreach references unknown node %q", n.ID, ref.NodeID)
	}
	if !contains(n.Deps, ref.NodeID) {
		return fmt.Errorf("node %q: foreach source %q must also be listed in deps", n.ID, ref.NodeID)
	}
	// v1: no nested scatter — a scatter node may not fan out over the output
	// of another scatter node.
	if src.IsScatter() {
		return fmt.Errorf("node %q: nested scatter not supported in v1 (foreach source %q is itself a scatter node)", n.ID, src.ID)
	}
	return nil
}

// checkAcyclic verifies the dependency graph has no cycles via DFS coloring.
func checkAcyclic(g *Graph, byID map[string]*Node) error {
	const (
		white = 0
		gray  = 1
		black = 2
	)
	color := make(map[string]int, len(g.Nodes))
	var stack []string

	var visit func(id string) error
	visit = func(id string) error {
		color[id] = gray
		stack = append(stack, id)
		for _, d := range byID[id].Deps {
			switch color[d] {
			case gray:
				cyc := append(append([]string{}, stack...), d)
				return fmt.Errorf("cycle detected: %s", strings.Join(cyc, " -> "))
			case white:
				if err := visit(d); err != nil {
					return err
				}
			}
		}
		stack = stack[:len(stack)-1]
		color[id] = black
		return nil
	}

	for _, n := range g.Nodes {
		if color[n.ID] == white {
			if err := visit(n.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

func checkDuration(s string) error {
	if s == "" {
		return nil
	}
	if _, err := time.ParseDuration(s); err != nil {
		return fmt.Errorf("invalid duration %q", s)
	}
	return nil
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}
