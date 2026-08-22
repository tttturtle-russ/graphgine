package executor

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/tttturtle-russ/graphgine/internal/graph"
)

func writeSkill(t *testing.T, root, name string) string {
	t.Helper()
	p := filepath.Join(root, name, "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("---\nname: "+name+"\n---\nbody\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	abs, _ := filepath.Abs(p)
	return abs
}

// A skill the host already registers resolves by name from a host skill root.
func TestHostRegisteredSkillResolvesByName(t *testing.T) {
	claudeDir := t.TempDir()
	want := writeSkill(t, filepath.Join(claudeDir, "skills"), "memory-corruption-triage")
	t.Setenv("CLAUDE_CONFIG_DIR", claudeDir)

	refs := resolveSkills(&graph.Node{Skills: []string{"memory-corruption-triage"}})
	if len(refs) != 1 {
		t.Fatalf("want one skill, got %#v", refs)
	}
	if refs[0].Source != srcUser {
		t.Fatalf("want it resolved from the user root, got %#v", refs[0])
	}
	if refs[0].Path != want {
		t.Fatalf("path = %q, want %q", refs[0].Path, want)
	}
}

// A node-local copy wins over a same-named host skill: it is the one that
// travels with the graph, and init loads it into the project root.
func TestNodeLocalSkillWins(t *testing.T) {
	claudeDir := t.TempDir()
	writeSkill(t, filepath.Join(claudeDir, "skills"), "triage") // same name, host-registered
	t.Setenv("CLAUDE_CONFIG_DIR", claudeDir)

	nodeDir := t.TempDir()
	local := writeSkill(t, filepath.Join(nodeDir, "skills"), "triage")

	refs := resolveSkills(&graph.Node{Dir: nodeDir, Skills: []string{"triage"}})
	if len(refs) != 1 {
		t.Fatalf("want one skill, got %#v", refs)
	}
	if refs[0].Source != srcNode {
		t.Fatalf("node-local copy must win, got %#v", refs[0])
	}
	if refs[0].Path != local {
		t.Fatalf("path = %q, want the node-local copy %q", refs[0].Path, local)
	}
}

// Nothing on disk: the host may still know the name (a plugin skill), so the
// ref carries just the name and the driver binds by it.
func TestUnresolvedSkillResolvesToNameOnly(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	t.Setenv("GRAPHGINE_SKILLS_DIR", t.TempDir())

	refs := resolveSkills(&graph.Node{Skills: []string{"nowhere-to-be-found"}})
	if len(refs) != 1 || refs[0].Path != "" || refs[0].Source != srcUnknown {
		t.Fatalf("want a name-only unresolved ref, got %#v", refs)
	}
}
