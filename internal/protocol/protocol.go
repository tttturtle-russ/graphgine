// Package protocol defines the JSON messages exchanged between the driving
// main agent and the CLI. All CLI subcommands print one of these as JSON.
package protocol

// SkillRef is a skill the driver attaches to an agent node's sub-agent. It says
// how to attach the skill, never what the skill says: the CLI does not read
// skill bodies, and the driver must not paste one into a prompt.
//
// The driver attaches it by name: it tells the sub-agent to use the /Name skill,
// and the sub-agent loads the body — and the skill's own tool permissions — into
// its own context via the Skill tool. `init` has loaded the skill into the
// project skill root so the name resolves. Path is where the skill lives on
// disk: informational, and a fallback the sub-agent can read directly if the
// name is not yet resolvable (e.g. a link newer than the session).
//
// The set is decided once, by `init`, and recorded in the run.
//
// Source records where it was resolved from: node | project | user |
// skills-dir | path | unresolved.
type SkillRef struct {
	Name   string `json:"name"`
	Path   string `json:"path,omitempty"`
	Source string `json:"source,omitempty"`
}

// SkillLink is one skill registration performed by `init`. The host discovers
// skills by directory name under the roots it scans, so a skill that ships
// inside a graph is not bindable until something puts it there. init links it
// in — the skill keeps living in the graph, and the host gains a name to bind.
//
// Status:
//   - "linked":     init created the link just now. The host scans its
//     skill roots when a session starts, so the name binds from the next
//     session; until then the driver falls back to Path.
//   - "registered": already in place, nothing to do.
//   - "conflict":   the name is taken by something else; left untouched.
//   - "failed":     the link could not be created (see Detail).
type SkillLink struct {
	Name   string `json:"name"`
	Path   string `json:"path"`           // the skill directory inside the graph
	Link   string `json:"link,omitempty"` // where it was registered
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

// NodeSpec is a unit of work the driver must execute: an `agent` node (spawn an
// isolated sub-agent) or a `human` node (solicit a decision). Scatter instances
// carry a bracketed NodeID such as "analyze[3]".
type NodeSpec struct {
	RunID        string         `json:"run_id"`
	NodeID       string         `json:"node_id"`
	BaseID       string         `json:"base_id"`
	Type         string         `json:"type"`            // agent | human
	Agent        string         `json:"agent,omitempty"` // sub-agent type to spawn (empty => driver default)
	Prompt       string         `json:"prompt,omitempty"`
	Skills       []SkillRef     `json:"skills,omitempty"` // skills to bind to the sub-agent
	Tools        []string       `json:"tools,omitempty"`
	Input        map[string]any `json:"input,omitempty"` // resolved scatter input (item/index substituted)
	InputDir     string         `json:"input_dir"`       // upstream outputs materialized here
	OutputDir    string         `json:"output_dir"`      // write output.json here (or pass via report --output)
	OutputSchema map[string]any `json:"output_schema,omitempty"`
	Attempt      int            `json:"attempt"`
}

// NextResult is returned by `next`.
//
// Status:
//   - "work":    Todo holds agent/human nodes to execute now.
//   - "waiting":  nothing to hand over, but dispatched work is awaiting report.
//   - "done":     every node reached a terminal state, no fatal failure.
//   - "failed":   a fatal node failed; the task is aborted.
type NextResult struct {
	RunID   string     `json:"run_id"`
	Status  string     `json:"status"`
	Todo    []NodeSpec `json:"todo"`
	Message string     `json:"message,omitempty"`
}

// InitResult is returned by `init`.
type InitResult struct {
	RunID    string      `json:"run_id"`
	Dir      string      `json:"dir"`
	Frontier []string    `json:"frontier"`
	Skills   []SkillLink `json:"skills,omitempty"`
	Message  string      `json:"message,omitempty"`
}

// ReportResult is returned by `report`.
type ReportResult struct {
	RunID        string   `json:"run_id"`
	NodeID       string   `json:"node_id"`
	Accepted     bool     `json:"accepted"` // false => output rejected (schema); node retried or failed
	Status       string   `json:"status"`   // resulting node status
	TaskFailed   bool     `json:"task_failed"`
	Message      string   `json:"message,omitempty"`
	SchemaErrors []string `json:"schema_errors,omitempty"`
}

// StatusNode is one row of `status`.
type StatusNode struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Status   string `json:"status"`
	Attempts int    `json:"attempts"`
	Message  string `json:"message,omitempty"`
}

// StatusResult is returned by `status`.
type StatusResult struct {
	RunID     string       `json:"run_id"`
	GraphName string       `json:"graph_name"`
	Failed    bool         `json:"failed"`
	Done      bool         `json:"done"`
	Nodes     []StatusNode `json:"nodes"`
}

// FinalizeResult is returned by `finalize`.
type FinalizeResult struct {
	RunID     string         `json:"run_id"`
	Succeeded bool           `json:"succeeded"`
	Summary   map[string]int `json:"summary"`
	ExitCode  int            `json:"exit_code"`
	// SkillsUnlinked are the graph skill names whose per-run project links
	// finalize removed (see executor.cleanupLinks).
	SkillsUnlinked []string `json:"skills_unlinked,omitempty"`
}
