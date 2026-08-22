package executor

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/tttturtle-russ/graphgine/internal/graph"
)

// pathNode is an agent node that declares its skill by path, the way a node
// package ships one.
func pathNode(t *testing.T, nodeDir, name string) *graph.Node {
	t.Helper()
	writeSkill(t, filepath.Join(nodeDir, "carried"), name)
	return &graph.Node{
		ID: "n", Type: graph.NodeAgent, Dir: nodeDir,
		Skills: []string{filepath.Join("carried", name)},
	}
}

// projectRun makes the process cwd a fresh, empty project directory and isolates
// the user/global skill roots to empty temp dirs, so init's links land in — and
// are discovered from — this project's .claude/skills and nothing leaks in from
// the host. Returns the project dir.
func projectRun(t *testing.T) string {
	t.Helper()
	proj := t.TempDir()
	t.Chdir(proj)
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GRAPHGINE_SKILLS_LINK_DIR", "")
	t.Setenv("GRAPHGINE_SKILLS_DIR", "")
	return proj
}

// A skill declared by path is not bindable on its own — the host never looks
// inside a graph. init loads it into the *project* skill root so the name
// resolves, while the skill keeps living in the graph.
func TestPathDeclaredSkillIsLoadedIntoProject(t *testing.T) {
	proj := projectRun(t)
	nodeDir := t.TempDir()
	node := pathNode(t, nodeDir, "triage")
	g := &graph.Graph{Nodes: []*graph.Node{node}}

	bindings, links := bindSkills(g, "")
	if len(links) != 1 || links[0].Status != linkCreated {
		t.Fatalf("want one created link, got %#v", links)
	}
	wantLink := filepath.Join(proj, ".claude", "skills", "triage")
	if links[0].Link != wantLink {
		t.Fatalf("linked into %q, want the project skill root %q", links[0].Link, wantLink)
	}
	if !sameDir(links[0].Link, filepath.Join(nodeDir, "carried", "triage")) {
		t.Fatal("the link must point at the graph's skill")
	}
	refs := bindings["n"]
	if len(refs) != 1 || refs[0].Name != "triage" {
		t.Fatalf("want one recorded binding named triage, got %#v", refs)
	}
	if refs[0].Path != filepath.Join(nodeDir, "carried", "triage", "SKILL.md") {
		t.Fatalf("path must point into the graph, got %q", refs[0].Path)
	}

	// A later run: the link is already in place, reported as registered.
	_, links = bindSkills(g, "")
	if len(links) != 1 || links[0].Status != linkRegistered {
		t.Fatalf("want the link reported as already registered, got %#v", links)
	}
}

// Someone else's skill of the same name is never overwritten: the graph's copy
// is left in the graph and reported as a conflict.
func TestBindSkillsLeavesAForeignNameAlone(t *testing.T) {
	proj := projectRun(t)
	theirs := writeSkill(t, filepath.Join(proj, ".claude", "skills"), "triage")
	node := pathNode(t, t.TempDir(), "triage")

	_, links := bindSkills(&graph.Graph{Nodes: []*graph.Node{node}}, "")
	if len(links) != 1 || links[0].Status != linkConflict {
		t.Fatalf("want a conflict, got %#v", links)
	}
	if _, err := os.Lstat(theirs); err != nil {
		t.Fatalf("the existing skill must survive: %v", err)
	}
}

// A link left behind by a graph that has since moved registers nothing and
// holds the name hostage, so it is replaced.
func TestBindSkillsReplacesABrokenLink(t *testing.T) {
	proj := projectRun(t)
	root := filepath.Join(proj, ".claude", "skills")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(t.TempDir(), "gone"), filepath.Join(root, "triage")); err != nil {
		t.Fatal(err)
	}
	nodeDir := t.TempDir()
	node := pathNode(t, nodeDir, "triage")

	_, links := bindSkills(&graph.Graph{Nodes: []*graph.Node{node}}, "")
	if len(links) != 1 || links[0].Status != linkCreated || links[0].Detail == "" {
		t.Fatalf("want the broken link replaced and explained, got %#v", links)
	}
	if !sameDir(filepath.Join(root, "triage"), filepath.Join(nodeDir, "carried", "triage")) {
		t.Fatal("the repaired link must point at the graph's skill")
	}
}

// Two nodes shipping different skills under one name: the first wins, the
// second is reported rather than silently rebound.
func TestBindSkillsReportsANameClaimedTwice(t *testing.T) {
	projectRun(t)
	a, b := pathNode(t, t.TempDir(), "triage"), pathNode(t, t.TempDir(), "triage")
	b.ID = "m"

	_, links := bindSkills(&graph.Graph{Nodes: []*graph.Node{a, b}}, "")
	if len(links) != 2 {
		t.Fatalf("want both claims reported, got %#v", links)
	}
	if links[0].Status != linkCreated || links[1].Status != linkConflict {
		t.Fatalf("want the first linked and the second flagged, got %#v", links)
	}
}

// cleanupLinks (what finalize runs) removes the links this run created, leaves
// the graph's own skill in place, and is safe to run twice.
func TestCleanupLinksRemovesOnlyOurCreatedLinks(t *testing.T) {
	projectRun(t)
	nodeDir := t.TempDir()
	g := &graph.Graph{Nodes: []*graph.Node{pathNode(t, nodeDir, "triage")}}

	_, links := bindSkills(g, "")
	link := links[0].Link
	if _, err := os.Lstat(link); err != nil {
		t.Fatalf("link should exist after bind: %v", err)
	}

	removed := cleanupLinks(links)
	if len(removed) != 1 || removed[0] != "triage" {
		t.Fatalf("want [triage] unlinked, got %v", removed)
	}
	if _, err := os.Lstat(link); !os.IsNotExist(err) {
		t.Fatalf("the project link should be gone, got err=%v", err)
	}
	// the skill itself, living in the graph, must survive.
	if _, err := os.Stat(filepath.Join(nodeDir, "carried", "triage", "SKILL.md")); err != nil {
		t.Fatalf("graph skill must survive cleanup: %v", err)
	}
	// idempotent: a second cleanup removes nothing.
	if r := cleanupLinks(links); len(r) != 0 {
		t.Fatalf("second cleanup should be a no-op, removed %v", r)
	}
}

// cleanupLinks never removes a link this run did not create (e.g. a foreign name
// reported as a conflict).
func TestCleanupLinksLeavesForeignLinksAlone(t *testing.T) {
	proj := projectRun(t)
	theirs := writeSkill(t, filepath.Join(proj, ".claude", "skills"), "triage")
	node := pathNode(t, t.TempDir(), "triage")

	_, links := bindSkills(&graph.Graph{Nodes: []*graph.Node{node}}, "")
	if links[0].Status != linkConflict {
		t.Fatalf("precondition: want a conflict, got %#v", links)
	}
	if r := cleanupLinks(links); len(r) != 0 {
		t.Fatalf("a conflict link is not ours to remove, removed %v", r)
	}
	if _, err := os.Lstat(theirs); err != nil {
		t.Fatalf("the foreign skill must survive cleanup: %v", err)
	}
}
