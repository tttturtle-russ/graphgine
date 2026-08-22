package executor

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/tttturtle-russ/graphgine/internal/graph"
	"github.com/tttturtle-russ/graphgine/internal/protocol"
	"github.com/tttturtle-russ/graphgine/internal/runstore"
)

// newRun parses+validates a YAML graph and creates an in-process run.
func newRun(t *testing.T, y string, concurrency int) *runstore.Run {
	t.Helper()
	g, err := graph.Parse([]byte(y))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := graph.Validate(g); err != nil {
		t.Fatalf("validate: %v", err)
	}
	run, err := runstore.NewStore(t.TempDir()).Create(g, concurrency)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	return run
}

// handler decides the outcome of an agent/human node the driver "executes".
type handler func(spec protocol.NodeSpec) (status string, output any)

// drive runs the main-agent loop to completion, delegating agent/human work to h.
func drive(t *testing.T, run *runstore.Run, h handler) string {
	t.Helper()
	for i := 0; i < 1000; i++ {
		res := Next(run)
		switch res.Status {
		case "work":
			for _, spec := range res.Todo {
				st, out := h(spec)
				if st == "success" && out != nil {
					if err := run.WriteOutput(spec.NodeID, out); err != nil {
						t.Fatalf("write output: %v", err)
					}
				}
				if _, err := Report(run, spec.NodeID, st, ""); err != nil {
					t.Fatalf("report %s: %v", spec.NodeID, err)
				}
			}
		case "done", "failed":
			return res.Status
		case "waiting":
			t.Fatalf("unexpected waiting: %s", res.Message)
		default:
			t.Fatalf("unexpected status %q", res.Status)
		}
	}
	t.Fatal("drive did not terminate")
	return ""
}

func noAgents(spec protocol.NodeSpec) (string, any) {
	panic("no agent/human nodes expected: " + spec.NodeID)
}

func status(run *runstore.Run, id string) runstore.Status {
	return run.State.Nodes[id].Status
}

// TestCommandPipeline: a pure command graph runs with zero driver involvement,
// and an upstream's output is materialized into the downstream's input.
func TestCommandPipeline(t *testing.T) {
	run := newRun(t, `
name: pipe
nodes:
  - id: a
    type: command
    command: ["sh","-c","echo '{\"n\": 7}'"]
    output_schema: { type: object, required: [n] }
  - id: b
    type: command
    deps: [a]
    command: ["sh","-c","cat \"$GRAPHGINE_INPUT_DIR/a/output.json\""]
    output_schema: { type: object, required: [n] }
`, 10)
	if got := drive(t, run, noAgents); got != "done" {
		t.Fatalf("want done, got %s", got)
	}
	out, _ := run.ReadOutput("b")
	m, ok := out.(map[string]any)
	if !ok || m["n"] != float64(7) {
		t.Fatalf("b did not receive a's output: %#v", out)
	}
}

// TestCascadeSkip: a non-fatal failure skips its dependents while an independent
// branch still runs; the task does not fail.
func TestCascadeSkip(t *testing.T) {
	run := newRun(t, `
name: cascade
nodes:
  - id: bad
    type: command
    command: ["sh","-c","exit 1"]
  - id: dep
    type: command
    deps: [bad]
    command: ["true"]
  - id: ok
    type: command
    command: ["sh","-c","echo '{}'"]
`, 10)
	if got := drive(t, run, noAgents); got != "done" {
		t.Fatalf("want done, got %s", got)
	}
	if status(run, "bad") != runstore.Failed {
		t.Fatalf("bad should be failed, got %s", status(run, "bad"))
	}
	if status(run, "dep") != runstore.Skipped {
		t.Fatalf("dep should be skipped, got %s", status(run, "dep"))
	}
	if status(run, "ok") != runstore.Success {
		t.Fatalf("ok should be success, got %s", status(run, "ok"))
	}
	if run.State.Failed {
		t.Fatal("task should not be failed (non-fatal)")
	}
}

// TestFatalFailure: a fatal node failing aborts the whole task.
func TestFatalFailure(t *testing.T) {
	run := newRun(t, `
name: fatal
nodes:
  - id: boom
    type: command
    fatal: true
    command: ["sh","-c","exit 3"]
  - id: after
    type: command
    deps: [boom]
    command: ["true"]
`, 10)
	if got := drive(t, run, noAgents); got != "failed" {
		t.Fatalf("want failed, got %s", got)
	}
	if !run.State.Failed {
		t.Fatal("task should be latched failed")
	}
	fin := Finalize(run)
	if fin.ExitCode != 1 || fin.Succeeded {
		t.Fatalf("finalize should report failure, got %#v", fin)
	}
}

// TestRetry: a repeatedly failing node with retry exhausts its attempts.
func TestRetry(t *testing.T) {
	run := newRun(t, `
name: retry
nodes:
  - id: flaky
    type: command
    retry: { max: 2 }
    command: ["sh","-c","exit 1"]
`, 10)
	if got := drive(t, run, noAgents); got != "done" {
		t.Fatalf("want done (non-fatal failure), got %s", got)
	}
	if a := run.State.Nodes["flaky"].Attempts; a != 3 {
		t.Fatalf("want 3 attempts (1 + 2 retries), got %d", a)
	}
	if status(run, "flaky") != runstore.Failed {
		t.Fatalf("flaky should be failed, got %s", status(run, "flaky"))
	}
}

// TestScatterGather: a command scatter fans out per element; even items fail
// (non-fatal) and are dropped; the downstream receives only survivors.
func TestScatterGather(t *testing.T) {
	run := newRun(t, `
name: scatter
nodes:
  - id: gen
    type: command
    command: ["sh","-c","echo '{\"items\": [1,2,3,4]}'"]
    output_schema: { type: object, required: [items] }
  - id: work
    type: command
    deps: [gen]
    foreach: ${gen.output.items}
    command: ["sh","-c","if [ $((GRAPHGINE_ITEM % 2)) -eq 0 ]; then exit 1; fi; echo \"{\\\"v\\\": $GRAPHGINE_ITEM}\""]
    output_schema: { type: object, required: [v] }
  - id: sink
    type: command
    deps: [work]
    command: ["sh","-c","cat \"$GRAPHGINE_INPUT_DIR/work/output.json\""]
`, 4)
	if got := drive(t, run, noAgents); got != "done" {
		t.Fatalf("want done, got %s", got)
	}
	if run.State.Failed {
		t.Fatal("task should not fail (non-fatal instances)")
	}
	// 4 instances created
	tmpl := run.State.Nodes["work"]
	if len(tmpl.Instances) != 4 {
		t.Fatalf("want 4 instances, got %d", len(tmpl.Instances))
	}
	// gather -> survivors [1,3]
	out, _ := run.ReadOutput("work")
	arr, ok := out.([]any)
	if !ok || len(arr) != 2 {
		t.Fatalf("want 2 survivors, got %#v", out)
	}
	// sink received the survivor array
	sinkOut, _ := run.ReadOutput("sink")
	sArr, ok := sinkOut.([]any)
	if !ok || len(sArr) != 2 {
		t.Fatalf("sink should receive 2 survivors, got %#v", sinkOut)
	}
}

// TestAgentAndHumanScatter: an agent scatter node and a human gate flow through
// the driver; gather feeds the human node.
func TestAgentAndHumanScatter(t *testing.T) {
	run := newRun(t, `
name: mixed
nodes:
  - id: gen
    type: command
    command: ["sh","-c","echo '{\"c\": [\"x\",\"y\"]}'"]
    output_schema: { type: object, required: [c] }
  - id: an
    type: agent
    deps: [gen]
    foreach: ${gen.output.c}
    input: { t: "${item}" }
    prompt: analyze the candidate
    output_schema: { type: object, required: [ok] }
  - id: rev
    type: human
    deps: [an]
    prompt: approve or reject
    output_schema: { type: object, required: [decision] }
`, 10)

	seen := map[string]any{}
	h := func(spec protocol.NodeSpec) (string, any) {
		switch spec.BaseID {
		case "an":
			seen[spec.NodeID] = spec.Input["t"]
			return "success", map[string]any{"ok": true, "t": spec.Input["t"]}
		case "rev":
			return "success", map[string]any{"decision": "approve"}
		}
		t.Fatalf("unexpected node %s", spec.NodeID)
		return "failed", nil
	}
	if got := drive(t, run, h); got != "done" {
		t.Fatalf("want done, got %s", got)
	}
	if len(seen) != 2 {
		t.Fatalf("want 2 agent instances, got %d: %#v", len(seen), seen)
	}
	// human received the gathered survivor array (2 items)
	revOut, _ := run.ReadOutput("rev")
	if m, ok := revOut.(map[string]any); !ok || m["decision"] != "approve" {
		t.Fatalf("rev output wrong: %#v", revOut)
	}
	// verify gather populated rev's input
	gathered, _ := run.ReadOutput("an")
	if arr, ok := gathered.([]any); !ok || len(arr) != 2 {
		t.Fatalf("an gather should have 2 survivors, got %#v", gathered)
	}
}

// TestScatterFatalInstanceFailsTask: a fatal scatter instance failing aborts.
func TestScatterFatalInstanceFailsTask(t *testing.T) {
	run := newRun(t, `
name: sfatal
nodes:
  - id: gen
    type: command
    command: ["sh","-c","echo '{\"items\": [1,2]}'"]
    output_schema: { type: object, required: [items] }
  - id: work
    type: command
    deps: [gen]
    fatal: true
    foreach: ${gen.output.items}
    command: ["sh","-c","if [ $((GRAPHGINE_ITEM % 2)) -eq 0 ]; then exit 1; fi; echo '{}'"]
`, 10)
	if got := drive(t, run, noAgents); got != "failed" {
		t.Fatalf("want failed (fatal instance), got %s", got)
	}
}

// TestReportSchemaRejection: a success report whose output fails the node's
// schema is rejected; with no retry the node fails.
func TestReportSchemaRejection(t *testing.T) {
	run := newRun(t, `
name: rej
nodes:
  - id: a
    type: agent
    prompt: do it
    output_schema: { type: object, required: [verdict] }
`, 10)
	res := Next(run)
	if res.Status != "work" || len(res.Todo) != 1 {
		t.Fatalf("want one work item, got %#v", res)
	}
	if err := run.WriteOutput("a", map[string]any{"nope": 1}); err != nil {
		t.Fatal(err)
	}
	rr, err := Report(run, "a", "success", "")
	if err != nil {
		t.Fatal(err)
	}
	if rr.Accepted {
		t.Fatal("report should be rejected (missing required 'verdict')")
	}
	if len(rr.SchemaErrors) == 0 {
		t.Fatal("expected schema errors in rejection")
	}
	if status(run, "a") != runstore.Failed {
		t.Fatalf("no-retry node should fail after rejection, got %s", status(run, "a"))
	}
}

// TestReportRejectionThenRetrySucceeds: a rejected output requeues the node
// (retry), and a subsequent valid report succeeds.
func TestReportRejectionThenRetrySucceeds(t *testing.T) {
	run := newRun(t, `
name: rejretry
nodes:
  - id: a
    type: agent
    prompt: do it
    retry: { max: 1 }
    output_schema: { type: object, required: [verdict] }
`, 10)
	// first dispatch + bad report -> requeued
	Next(run)
	run.WriteOutput("a", map[string]any{"nope": 1})
	if rr, _ := Report(run, "a", "success", ""); rr.Accepted || rr.Status != string(runstore.Pending) {
		t.Fatalf("want requeue to pending, got %#v", rr)
	}
	// second dispatch + good report -> success
	res := Next(run)
	if res.Status != "work" || len(res.Todo) != 1 {
		t.Fatalf("want re-dispatch, got %#v", res)
	}
	run.WriteOutput("a", map[string]any{"verdict": "exploitable"})
	if rr, _ := Report(run, "a", "success", ""); !rr.Accepted {
		t.Fatalf("valid report should be accepted, got %#v", rr)
	}
	if status(run, "a") != runstore.Success {
		t.Fatalf("want success, got %s", status(run, "a"))
	}
}

// TestNodePackageSkillResolution: a node-package graph auto-loads a node-local
// skill; its resolved absolute path is handed to the driver in the spec.
func TestNodePackageSkillResolution(t *testing.T) {
	dir := t.TempDir()
	mk := func(p, c string) {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk(filepath.Join(dir, "graph.yaml"), "name: skilltest\n")
	mk(filepath.Join(dir, "nodes", "gen", "node.yaml"),
		"type: command\ncommand: [\"sh\",\"-c\",\"echo '{\\\"c\\\":[\\\"x\\\"]}'\"]\noutput_schema: { type: object, required: [c] }\n")
	mk(filepath.Join(dir, "nodes", "an", "node.yaml"),
		"type: agent\ndeps: [gen]\nforeach: ${gen.output.c}\ninput: { t: \"${item}\" }\noutput_schema: { type: object }\n")
	mk(filepath.Join(dir, "nodes", "an", "prompt.md"), "do the thing")
	mk(filepath.Join(dir, "nodes", "an", "skills", "triage", "SKILL.md"), "---\nname: triage\n---\nbody\n")

	// init reads the node's skills, registers them, and records the bindings;
	// every later spawn attaches them from that record. Isolate cwd so init's
	// project-root links land in a throwaway dir, not the source tree.
	t.Chdir(t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	store := runstore.NewStore(t.TempDir())
	initRes, err := Init(store, dir, InitOptions{Concurrency: 10})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if len(initRes.Skills) != 1 || initRes.Skills[0].Name != "triage" {
		t.Fatalf("init must register the node's own skill, got %#v", initRes.Skills)
	}
	run, err := store.Open(initRes.RunID)
	if err != nil {
		t.Fatal(err)
	}
	defer run.Close()
	res := Next(run) // runs gen, expands an, hands back an[0]
	if res.Status != "work" || len(res.Todo) != 1 {
		t.Fatalf("want one agent work item, got %#v", res)
	}
	skills := res.Todo[0].Skills
	if len(skills) != 1 || skills[0].Name != "triage" {
		t.Fatalf("want the auto-discovered 'triage' skill, got %#v", skills)
	}
	if skills[0].Path == "" || filepath.Base(skills[0].Path) != "SKILL.md" {
		t.Fatalf("skill path not resolved to a SKILL.md: %q", skills[0].Path)
	}
}
