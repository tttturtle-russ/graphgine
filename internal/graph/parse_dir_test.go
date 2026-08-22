package graph

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestParseDir(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "graph.yaml"), "name: pkg-demo\n")
	write(t, filepath.Join(dir, "nodes", "recon", "node.yaml"),
		"type: command\ncommand: [\"true\"]\noutput_schema: { type: object }\n")
	write(t, filepath.Join(dir, "nodes", "analyze", "node.yaml"),
		"type: agent\ndeps: [recon]\nforeach: ${recon.output.items}\ninput: { x: \"${item}\" }\noutput_schema: { type: object }\n")
	write(t, filepath.Join(dir, "nodes", "analyze", "prompt.md"), "Analyze the item thoroughly.\n")
	write(t, filepath.Join(dir, "nodes", "analyze", "skills", "triage", "SKILL.md"), "---\nname: triage\n---\nbody\n")

	g, err := ParseDir(dir)
	if err != nil {
		t.Fatalf("ParseDir: %v", err)
	}
	if err := Validate(g); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if g.Name != "pkg-demo" {
		t.Fatalf("name = %q", g.Name)
	}
	an := g.Node("analyze")
	if an == nil {
		t.Fatal("analyze node missing")
	}
	if an.ID != "analyze" {
		t.Fatalf("id from dir name = %q", an.ID)
	}
	if an.Prompt != "Analyze the item thoroughly." {
		t.Fatalf("prompt.md not loaded: %q", an.Prompt)
	}
	if an.Dir == "" {
		t.Fatal("node Dir not set")
	}
	if g.Node("recon") == nil {
		t.Fatal("recon node missing")
	}
}

func TestParseDirNoNodes(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "graph.yaml"), "name: empty\n")
	if _, err := ParseDir(dir); err == nil {
		t.Fatal("expected error for a package with no nodes")
	}
}

func TestParseDirExplicitTopology(t *testing.T) {
	dir := t.TempDir()
	// graph.yaml owns the structure (node list + edges); node.yaml files carry
	// only behavior and declare no deps.
	write(t, filepath.Join(dir, "graph.yaml"),
		"name: explicit-demo\nnodes:\n  - id: recon\n  - id: sink-scan\n    deps: [recon]\n  - id: analyze\n    deps: [sink-scan]\n")
	write(t, filepath.Join(dir, "nodes", "recon", "node.yaml"),
		"type: command\ncommand: [\"true\"]\noutput_schema: { type: object, required: [items] }\n")
	write(t, filepath.Join(dir, "nodes", "sink-scan", "node.yaml"),
		"type: command\ncommand: [\"true\"]\noutput_schema: { type: object }\n")
	write(t, filepath.Join(dir, "nodes", "analyze", "node.yaml"),
		"type: agent\nforeach: ${sink-scan.output.items}\ninput: { x: \"${item}\" }\noutput_schema: { type: object }\n")
	write(t, filepath.Join(dir, "nodes", "analyze", "prompt.md"), "Analyze.\n")

	g, err := ParseDir(dir)
	if err != nil {
		t.Fatalf("ParseDir: %v", err)
	}
	if err := Validate(g); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if g.Name != "explicit-demo" {
		t.Fatalf("name = %q", g.Name)
	}
	// declaration order is preserved
	gotOrder := []string{g.Nodes[0].ID, g.Nodes[1].ID, g.Nodes[2].ID}
	want := []string{"recon", "sink-scan", "analyze"}
	for i := range want {
		if gotOrder[i] != want[i] {
			t.Fatalf("order[%d] = %q, want %q", i, gotOrder[i], want[i])
		}
	}
	// deps come from graph.yaml
	ss := g.Node("sink-scan")
	if len(ss.Deps) != 1 || ss.Deps[0] != "recon" {
		t.Fatalf("sink-scan deps = %v, want [recon]", ss.Deps)
	}
	an := g.Node("analyze")
	if len(an.Deps) != 1 || an.Deps[0] != "sink-scan" {
		t.Fatalf("analyze deps = %v, want [sink-scan]", an.Deps)
	}
	if an.Prompt != "Analyze." {
		t.Fatalf("prompt.md not loaded: %q", an.Prompt)
	}
	if an.Dir == "" {
		t.Fatal("node Dir not set")
	}
}

func TestParseDirExplicitBareStringEntry(t *testing.T) {
	dir := t.TempDir()
	// A bare-string entry is a node with no deps.
	write(t, filepath.Join(dir, "graph.yaml"), "name: bare\nnodes:\n  - recon\n")
	write(t, filepath.Join(dir, "nodes", "recon", "node.yaml"),
		"type: command\ncommand: [\"true\"]\n")
	g, err := ParseDir(dir)
	if err != nil {
		t.Fatalf("ParseDir: %v", err)
	}
	if g.Node("recon") == nil || len(g.Node("recon").Deps) != 0 {
		t.Fatalf("bare entry not parsed as a depless node: %+v", g.Nodes)
	}
}

func TestParseDirExplicitRejectsNodeDeps(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "graph.yaml"),
		"name: dup-deps\nnodes:\n  - id: recon\n  - id: analyze\n    deps: [recon]\n")
	write(t, filepath.Join(dir, "nodes", "recon", "node.yaml"),
		"type: command\ncommand: [\"true\"]\n")
	// node.yaml illegally re-declares deps in explicit mode.
	write(t, filepath.Join(dir, "nodes", "analyze", "node.yaml"),
		"type: agent\ndeps: [recon]\nprompt: hi\noutput_schema: { type: object }\n")
	if _, err := ParseDir(dir); err == nil {
		t.Fatal("expected error: deps in node.yaml under explicit topology")
	}
}

func TestParseDirExplicitMissingNodeDir(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "graph.yaml"),
		"name: missing\nnodes:\n  - id: recon\n  - id: ghost\n")
	write(t, filepath.Join(dir, "nodes", "recon", "node.yaml"),
		"type: command\ncommand: [\"true\"]\n")
	if _, err := ParseDir(dir); err == nil {
		t.Fatal("expected error: listed node has no nodes/<id>/node.yaml")
	}
}
