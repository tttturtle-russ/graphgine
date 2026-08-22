package graph

import (
	"strings"
	"testing"
)

func mustParse(t *testing.T, y string) *Graph {
	t.Helper()
	g, err := Parse([]byte(y))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return g
}

func wantErr(t *testing.T, y, substr string) {
	t.Helper()
	err := Validate(mustParse(t, y))
	if err == nil {
		t.Fatalf("expected error containing %q, got nil", substr)
	}
	if substr != "" && !strings.Contains(err.Error(), substr) {
		t.Fatalf("error %q does not contain %q", err.Error(), substr)
	}
}

func TestValidateOK(t *testing.T) {
	y := `
name: ok
nodes:
  - id: a
    type: command
    command: ["true"]
  - id: b
    type: agent
    deps: [a]
    prompt: do it
  - id: c
    type: agent
    deps: [a]
    foreach: ${a.output.items}
    input: { x: "${item}" }
    prompt: per item
`
	if err := Validate(mustParse(t, y)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateCycle(t *testing.T) {
	wantErr(t, `
name: c
nodes:
  - id: a
    type: command
    command: ["true"]
    deps: [b]
  - id: b
    type: command
    command: ["true"]
    deps: [a]
`, "cycle")
}

func TestValidateDanglingDep(t *testing.T) {
	wantErr(t, `
name: d
nodes:
  - id: a
    type: command
    command: ["true"]
    deps: [nope]
`, "does not exist")
}

func TestValidateNestedScatter(t *testing.T) {
	wantErr(t, `
name: ns
nodes:
  - id: a
    type: command
    command: ["true"]
  - id: b
    type: command
    command: ["true"]
    deps: [a]
    foreach: ${a.output.items}
  - id: c
    type: command
    command: ["true"]
    deps: [b]
    foreach: ${b.output.rows}
`, "nested scatter")
}

func TestValidateBadType(t *testing.T) {
	wantErr(t, `
name: bt
nodes:
  - id: a
    type: wizard
    command: ["true"]
`, "unknown type")
}

func TestValidateCommandRequiresCommand(t *testing.T) {
	wantErr(t, `
name: cc
nodes:
  - id: a
    type: command
`, "requires a non-empty command")
}

func TestValidateForeachNotInDeps(t *testing.T) {
	wantErr(t, `
name: fd
nodes:
  - id: a
    type: command
    command: ["true"]
  - id: b
    type: command
    command: ["true"]
    foreach: ${a.output.items}
`, "must also be listed in deps")
}

// `agent:` picks the sub-agent type a node's skills are bound to, so it only
// means anything on an agent node.
func TestAgentFieldOnlyOnAgentNodes(t *testing.T) {
	wantErr(t, `
name: g
nodes:
  - id: a
    type: command
    agent: my-node-worker
    command: ["true"]
`, "agent: applies to agent nodes only")

	wantErr(t, `
name: g
nodes:
  - id: a
    type: human
    agent: my-node-worker
    prompt: approve?
`, "agent: applies to agent nodes only")
}
