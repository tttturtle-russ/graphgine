// Package executor is the scheduling core: it walks the declared DAG, executes
// command nodes directly, expands and gathers scatter nodes, propagates
// failures via cascade-skip, and hands agent/human work to the driving main
// agent. It contains zero LLM logic — it is a deterministic state machine.
package executor

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/tttturtle-russ/graphgine/internal/graph"
	"github.com/tttturtle-russ/graphgine/internal/protocol"
	"github.com/tttturtle-russ/graphgine/internal/runstore"
	"github.com/tttturtle-russ/graphgine/internal/schema"
)

// InitOptions configures a new run.
type InitOptions struct {
	Concurrency int
	// SkillsDir is the host skill root to register the graph's skills into
	// (empty => defaultSkillsDir).
	SkillsDir string
}

// Init parses and validates a graph (a single YAML file or a node-package
// directory), reads each agent node's skills and registers them with the host,
// then creates a fresh run holding those bindings. Every later spawn attaches
// the skills from that record.
func Init(store *runstore.Store, graphPath string, opts InitOptions) (protocol.InitResult, error) {
	g, err := graph.Load(graphPath)
	if err != nil {
		return protocol.InitResult{}, err
	}
	if err := graph.Validate(g); err != nil {
		return protocol.InitResult{}, err
	}
	bindings, links := bindSkills(g, opts.SkillsDir)
	run, err := store.Create(g, opts.Concurrency)
	if err != nil {
		return protocol.InitResult{}, err
	}
	run.State.Skills = bindings
	run.State.SkillLinks = links
	if err := run.Save(); err != nil {
		return protocol.InitResult{}, err
	}
	res := protocol.InitResult{
		RunID: run.State.ID, Dir: run.Dir,
		Skills: links, Message: linkMessage(links),
	}
	for _, n := range g.Nodes {
		if len(n.Deps) == 0 {
			res.Frontier = append(res.Frontier, n.ID)
		}
	}
	return res, nil
}

type depDecision int

const (
	depWait depDecision = iota
	depRun
	depSkip
)

// evalDeps decides whether a pending node can run, must wait, or must be
// skipped, based on its dependencies' current statuses.
func evalDeps(st *runstore.State, ns *runstore.NodeState) depDecision {
	ready := true
	for _, d := range ns.Deps {
		dep := st.Nodes[d]
		optional := contains(ns.Optional, d)
		switch {
		case dep == nil:
			return depSkip // shouldn't happen post-validation, but be safe
		case dep.Status == runstore.Failed || dep.Status == runstore.Skipped:
			if !optional {
				return depSkip
			}
			// optional + terminal-absent: fine, input will be marked ABSENT
		case dep.Status == runstore.Success:
			// satisfied
		default: // pending / running
			ready = false
		}
	}
	if !ready {
		return depWait
	}
	return depRun
}

// Next drives the run forward: it executes ready command nodes, expands/gathers
// scatter nodes, cascades skips, and returns the next agent/human work (if any).
func Next(r *runstore.Run) protocol.NextResult {
	st := r.State
	for {
		if st.Failed {
			return protocol.NextResult{RunID: st.ID, Status: "failed", Message: "task failed (fatal node)"}
		}
		progressed := false

		// (1) expand scatter templates whose deps are satisfied
		for _, id := range append([]string{}, st.Order...) {
			ns := st.Nodes[id]
			if !ns.IsScatterTemplate || ns.Expanded || ns.Status != runstore.Pending {
				continue
			}
			switch evalDeps(st, ns) {
			case depSkip:
				markSkipped(ns)
				progressed = true
			case depRun:
				if err := expandScatter(r, ns); err != nil {
					failNode(st, ns, "scatter expansion failed: "+err.Error())
				}
				progressed = true
			}
		}

		// (2) gather expanded scatter templates whose instances all settled
		for _, id := range append([]string{}, st.Order...) {
			ns := st.Nodes[id]
			if ns.IsScatterTemplate && ns.Expanded && ns.Status == runstore.Running {
				if gatherIfReady(r, ns) {
					progressed = true
				}
			}
		}

		// (3) cascade-skip pending non-template nodes with dead deps
		for _, id := range append([]string{}, st.Order...) {
			ns := st.Nodes[id]
			if ns.IsScatterTemplate || ns.Status != runstore.Pending {
				continue
			}
			if evalDeps(st, ns) == depSkip {
				markSkipped(ns)
				progressed = true
			}
		}

		// (4) collect runnable command vs agent/human work
		var cmdReady, ahReady []*runstore.NodeState
		for _, id := range append([]string{}, st.Order...) {
			ns := st.Nodes[id]
			if ns.IsScatterTemplate || ns.Status != runstore.Pending {
				continue
			}
			if evalDeps(st, ns) != depRun {
				continue
			}
			if ns.Type == graph.NodeCommand {
				cmdReady = append(cmdReady, ns)
			} else {
				ahReady = append(ahReady, ns)
			}
		}

		slots := st.Concurrency - countInFlight(st)
		if slots < 0 {
			slots = 0
		}

		if len(cmdReady) > 0 && slots > 0 {
			execCommandBatch(r, capBatch(cmdReady, slots))
			progressed = true
			continue // re-loop to advance downstream deterministically
		}

		if len(ahReady) > 0 && slots > 0 {
			var todo []protocol.NodeSpec
			for _, ns := range capBatch(ahReady, slots) {
				spec, err := dispatch(r, ns)
				if err != nil {
					failNode(st, ns, "prepare inputs failed: "+err.Error())
					continue
				}
				todo = append(todo, spec)
			}
			if len(todo) > 0 {
				return protocol.NextResult{RunID: st.ID, Status: "work", Todo: todo}
			}
			progressed = true
			continue
		}

		if st.Failed {
			return protocol.NextResult{RunID: st.ID, Status: "failed", Message: "task failed (fatal node)"}
		}
		if !progressed {
			if allTerminal(st) {
				return protocol.NextResult{RunID: st.ID, Status: "done"}
			}
			return protocol.NextResult{RunID: st.ID, Status: "waiting", Message: "awaiting reports for dispatched work"}
		}
	}
}

// dispatch prepares a node's inputs, marks it running, and builds the spec the
// driver needs to execute it.
func dispatch(r *runstore.Run, ns *runstore.NodeState) (protocol.NodeSpec, error) {
	node := r.State.Graph.Node(ns.BaseID)
	if err := prepareInputs(r, ns, node); err != nil {
		return protocol.NodeSpec{}, err
	}
	ns.Status = runstore.Running
	spec := protocol.NodeSpec{
		RunID:        r.State.ID,
		NodeID:       ns.ID,
		BaseID:       ns.BaseID,
		Type:         string(ns.Type),
		Agent:        node.Agent,
		Prompt:       node.Prompt,
		Skills:       r.State.Skills[ns.BaseID],
		Tools:        node.Tools,
		InputDir:     r.InputDir(ns.ID),
		OutputDir:    r.OutputDir(ns.ID),
		OutputSchema: node.OutputSchema,
		Attempt:      ns.Attempts,
	}
	if ns.Index >= 0 {
		resolved, err := resolveInput(r, ns, node)
		if err != nil {
			return protocol.NodeSpec{}, err
		}
		spec.Input = resolved
	}
	return spec, nil
}

// expandScatter deterministically expands a scatter template into one instance
// per element of the referenced upstream array.
func expandScatter(r *runstore.Run, ns *runstore.NodeState) error {
	node := r.State.Graph.Node(ns.BaseID)
	ref, _, err := graph.ParseRef(node.Foreach)
	if err != nil {
		return err
	}
	srcOut, err := r.ReadOutput(ref.NodeID)
	if err != nil {
		return err
	}
	arrAny, err := navigate(srcOut, ref.Path)
	if err != nil {
		return fmt.Errorf("foreach %q: %w", node.Foreach, err)
	}
	items, ok := arrAny.([]any)
	if !ok {
		return fmt.Errorf("foreach %q is not an array", node.Foreach)
	}

	for i, item := range items {
		instID := fmt.Sprintf("%s[%d]", ns.BaseID, i)
		inst := &runstore.NodeState{
			ID: instID, BaseID: ns.BaseID, Type: ns.Type, Status: runstore.Pending,
			Fatal: ns.Fatal, Deps: append([]string{}, node.Deps...),
			Optional: append([]string{}, node.OptionalInputs...), Index: i,
		}
		r.State.Nodes[instID] = inst
		r.State.Order = append(r.State.Order, instID)
		ns.Instances = append(ns.Instances, instID)
		if err := r.EnsureWorkspace(instID); err != nil {
			return err
		}
		if err := writeItem(r, instID, item); err != nil {
			return err
		}
	}
	ns.Expanded = true
	ns.Status = runstore.Running // gathering
	return nil
}

// gatherIfReady checks whether all of a scatter template's instances have
// settled; if so it performs the gather (barrier + survivors) and returns true.
func gatherIfReady(r *runstore.Run, ns *runstore.NodeState) bool {
	for _, iid := range ns.Instances {
		if !r.State.Nodes[iid].Status.Terminal() {
			return false
		}
	}
	// Fatal instance failure fails the whole scatter node (and, if the node is
	// fatal, the task was already latched at report/exec time).
	failedFatal := false
	survivors := make([]any, 0, len(ns.Instances))
	for _, iid := range ns.Instances {
		inst := r.State.Nodes[iid]
		if inst.Status == runstore.Success {
			out, _ := r.ReadOutput(iid)
			survivors = append(survivors, out)
		} else if inst.Status == runstore.Failed && ns.Fatal {
			failedFatal = true
		}
	}
	if failedFatal {
		ns.Status = runstore.Failed
		ns.Message = "one or more fatal scatter instances failed"
		r.State.Failed = true
		return true
	}
	if err := r.WriteOutput(ns.ID, survivors); err != nil {
		failNode(r.State, ns, "gather write failed: "+err.Error())
		return true
	}
	ns.Status = runstore.Success
	ns.Message = fmt.Sprintf("gathered %d/%d survivors", len(survivors), len(ns.Instances))
	return true
}

// Report records the outcome of an agent/human node the driver executed.
func Report(r *runstore.Run, nodeID, status, outputPath string) (protocol.ReportResult, error) {
	st := r.State
	ns := st.Nodes[nodeID]
	if ns == nil {
		return protocol.ReportResult{}, fmt.Errorf("unknown node %q", nodeID)
	}
	if ns.Status != runstore.Running {
		return protocol.ReportResult{}, fmt.Errorf("node %q is %q, not running (nothing to report)", nodeID, ns.Status)
	}
	node := st.Graph.Node(ns.BaseID)

	switch status {
	case "success":
		out, err := readReportedOutput(r, nodeID, outputPath)
		if err != nil {
			return protocol.ReportResult{}, err
		}
		if errs := schema.Validate(node.OutputSchema, out); len(errs) > 0 {
			res := handleFailure(r, ns, node, "reported output failed schema validation")
			res.Accepted = false
			res.SchemaErrors = errsToStrings(errs)
			return res, nil
		}
		if err := r.WriteOutput(nodeID, out); err != nil {
			return protocol.ReportResult{}, err
		}
		ns.Status = runstore.Success
		ns.Message = ""
		return protocol.ReportResult{RunID: st.ID, NodeID: nodeID, Accepted: true, Status: string(runstore.Success)}, nil

	case "failed":
		res := handleFailure(r, ns, node, "reported failed by driver")
		res.Accepted = true
		return res, nil

	default:
		return protocol.ReportResult{}, fmt.Errorf("invalid status %q (want success|failed)", status)
	}
}

// readReportedOutput loads the driver-provided output: an explicit --output
// file if given, else the output.json the sub-agent wrote into OutputDir.
func readReportedOutput(r *runstore.Run, nodeID, outputPath string) (any, error) {
	if outputPath != "" {
		data, err := os.ReadFile(outputPath)
		if err != nil {
			return nil, fmt.Errorf("read --output %q: %w", outputPath, err)
		}
		var v any
		if err := json.Unmarshal(data, &v); err != nil {
			return nil, fmt.Errorf("--output %q is not valid json: %w", outputPath, err)
		}
		return v, nil
	}
	return r.ReadOutput(nodeID)
}

// handleFailure applies retry-or-fail semantics and latches a fatal task
// failure when a fatal node exhausts its retries.
func handleFailure(r *runstore.Run, ns *runstore.NodeState, node *graph.Node, msg string) protocol.ReportResult {
	ns.Attempts++
	if node.Retry != nil && ns.Attempts <= node.Retry.Max {
		ns.Status = runstore.Pending
		ns.Message = fmt.Sprintf("%s (retry %d/%d queued)", msg, ns.Attempts, node.Retry.Max)
		return protocol.ReportResult{RunID: r.State.ID, NodeID: ns.ID, Status: string(runstore.Pending), Message: ns.Message}
	}
	ns.Status = runstore.Failed
	ns.Message = msg
	if ns.Fatal {
		r.State.Failed = true
	}
	return protocol.ReportResult{
		RunID: r.State.ID, NodeID: ns.ID, Status: string(runstore.Failed),
		TaskFailed: r.State.Failed, Message: msg,
	}
}

// Status renders a snapshot of the run for observability.
func Status(r *runstore.Run) protocol.StatusResult {
	st := r.State
	res := protocol.StatusResult{RunID: st.ID, GraphName: st.GraphName, Failed: st.Failed, Done: allTerminal(st)}
	for _, id := range st.Order {
		ns := st.Nodes[id]
		res.Nodes = append(res.Nodes, protocol.StatusNode{
			ID: ns.ID, Type: string(ns.Type), Status: string(ns.Status),
			Attempts: ns.Attempts, Message: ns.Message,
		})
	}
	return res
}

// Finalize summarizes the run and computes an exit code (0 unless a fatal node
// failed). Non-fatal failures do not fail the task (decision: fatal-ness alone
// decides task fate).
func Finalize(r *runstore.Run) protocol.FinalizeResult {
	st := r.State
	summary := map[string]int{}
	for _, id := range st.Order {
		summary[string(st.Nodes[id].Status)]++
	}
	succeeded := !st.Failed
	exit := 0
	if !succeeded {
		exit = 1
	}
	// Undo init's per-run skill links so the graph's bundled skills do not
	// outlive the run in the project skill root.
	unlinked := cleanupLinks(st.SkillLinks)
	return protocol.FinalizeResult{
		RunID: st.ID, Succeeded: succeeded, Summary: summary,
		ExitCode: exit, SkillsUnlinked: unlinked,
	}
}

// --- small helpers ---

func markSkipped(ns *runstore.NodeState) {
	ns.Status = runstore.Skipped
	ns.Message = "a required dependency failed or was skipped"
}

func failNode(st *runstore.State, ns *runstore.NodeState, msg string) {
	ns.Status = runstore.Failed
	ns.Message = msg
	if ns.Fatal {
		st.Failed = true
	}
}

// countInFlight counts nodes consuming a concurrency slot: running command or
// dispatched agent/human instances. Gathering scatter templates do not count.
func countInFlight(st *runstore.State) int {
	n := 0
	for _, ns := range st.Nodes {
		if ns.Status == runstore.Running && !ns.IsScatterTemplate {
			n++
		}
	}
	return n
}

func allTerminal(st *runstore.State) bool {
	for _, ns := range st.Nodes {
		if !ns.Status.Terminal() {
			return false
		}
	}
	return true
}

func capBatch(b []*runstore.NodeState, n int) []*runstore.NodeState {
	if len(b) > n {
		return b[:n]
	}
	return b
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

func errsToStrings(errs []error) []string {
	out := make([]string, 0, len(errs))
	for _, e := range errs {
		out = append(out, e.Error())
	}
	return out
}
