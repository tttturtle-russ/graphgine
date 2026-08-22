// Package graph defines the declarative graph/node model and its validation.
//
// A Graph is a static, user-declared DAG of Nodes. The declared graph is
// immutable at runtime; only `scatter` nodes are deterministically expanded
// into N instances from upstream array data (see the executor). Agents never
// mutate the topology.
package graph

// NodeType is the closed set of node kinds understood by the engine (v1).
type NodeType string

const (
	// NodeAgent is executed by the main agent, which spawns an isolated
	// sub-agent loaded with the node's skills/tools/input.
	NodeAgent NodeType = "agent"
	// NodeCommand is a deterministic program executed directly by the CLI
	// (zero LLM in the loop).
	NodeCommand NodeType = "command"
	// NodeHuman is a review gate: the main agent solicits a human decision.
	NodeHuman NodeType = "human"
)

// Retry declares how many times a failed node is re-run before it is marked
// failed, and the backoff between attempts.
type Retry struct {
	Max     int    `yaml:"max" json:"max"`
	Backoff string `yaml:"backoff,omitempty" json:"backoff,omitempty"` // Go duration, e.g. "5s"
}

// Node is a single declared subtask. Fields are shared plus type-specific;
// scatter fields are optional and apply to any type.
type Node struct {
	ID   string   `yaml:"id" json:"id"`
	Type NodeType `yaml:"type" json:"type"`
	Deps []string `yaml:"deps,omitempty" json:"deps,omitempty"`

	// Fatal: if this node fails (retries exhausted) the whole task fails.
	Fatal          bool     `yaml:"fatal,omitempty" json:"fatal,omitempty"`
	Retry          *Retry   `yaml:"retry,omitempty" json:"retry,omitempty"`
	OptionalInputs []string `yaml:"optional_inputs,omitempty" json:"optional_inputs,omitempty"`

	// OutputSchema is a JSON Schema (subset) describing a *single* node's
	// output. For scatter nodes it describes one instance's output.
	OutputSchema map[string]any `yaml:"output_schema,omitempty" json:"output_schema,omitempty"`

	// --- scatter (optional, type-orthogonal) ---
	// Foreach references an upstream array output, e.g.
	// "${sink-scan.output.candidates}". When set, the node is expanded at
	// runtime into one instance per element.
	Foreach string `yaml:"foreach,omitempty" json:"foreach,omitempty"`
	// Input is injected into each instance; ${item} / ${index} are substituted.
	Input map[string]any `yaml:"input,omitempty" json:"input,omitempty"`

	// --- agent ---
	Prompt string `yaml:"prompt,omitempty" json:"prompt,omitempty"`
	// Skills are bound to this node's sub-agent, never pasted into a prompt.
	// An entry is either a path to a skill directory (or its SKILL.md),
	// resolved relative to the node package, or the name of a skill the host
	// already knows. Either way the skill's directory name is the name it
	// binds under. Skills under <Dir>/skills/ are picked up automatically.
	Skills []string `yaml:"skills,omitempty" json:"skills,omitempty"`
	Tools  []string `yaml:"tools,omitempty" json:"tools,omitempty"`
	// Agent names the sub-agent type the driver spawns for this node. Empty
	// means the driver's default node worker. Point it at an agent definition
	// whose own frontmatter preloads this node's skills when you want the
	// binding to happen at spawn time rather than by name in the prompt.
	Agent string `yaml:"agent,omitempty" json:"agent,omitempty"`

	// --- command ---
	Command []string          `yaml:"command,omitempty" json:"command,omitempty"`
	Env     map[string]string `yaml:"env,omitempty" json:"env,omitempty"`
	Timeout string            `yaml:"timeout,omitempty" json:"timeout,omitempty"` // Go duration

	// Dir is the node package directory, set by the directory loader (empty for
	// inline nodes declared in a single graph file). It anchors resolution of
	// node-local skills (<Dir>/skills/*) and prompt.md.
	Dir string `yaml:"-" json:"dir,omitempty"`
}

// IsScatter reports whether this node fans out over an upstream array.
func (n *Node) IsScatter() bool { return n.Foreach != "" }

// Graph is the top-level declared structure.
type Graph struct {
	Name  string  `yaml:"name" json:"name"`
	Nodes []*Node `yaml:"nodes" json:"nodes"`
}

// Node returns the node with the given id, or nil.
func (g *Graph) Node(id string) *Node {
	for _, n := range g.Nodes {
		if n.ID == id {
			return n
		}
	}
	return nil
}
