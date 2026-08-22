package executor

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/tttturtle-russ/graphgine/internal/graph"
	"github.com/tttturtle-russ/graphgine/internal/protocol"
)

// Where a skill was resolved from. Reported in SkillRef.Source so the driver
// (and a human reading the JSON) can see where the skill on disk lives.
const (
	srcNode      = "node"       // <node.Dir>/skills/<name> — travels with the node package
	srcProject   = "project"    // <cwd>/.claude/skills/<name>
	srcUser      = "user"       // ~/.claude/skills/<name>
	srcSkillsDir = "skills-dir" // $GRAPHGINE_SKILLS_DIR or ./skills
	srcPath      = "path"       // an explicit path written in node.yaml
	srcUnknown   = "unresolved" // not found on disk; the host may still know the name
)

// skillRoot is one directory searched for a bare skill name.
type skillRoot struct{ dir, source string }

// resolveSkills builds the resolved skill set for an agent node: every skill
// directory found under <node.Dir>/skills/, plus every path or name listed in
// node.Skills.
//
// Each entry says *how the driver attaches the skill to the node's sub-agent* —
// by name (the driver tells the sub-agent to use the /Name skill) — never what
// the skill says: the CLI does not read skill bodies and the driver must not
// paste one into a prompt. init loads each skill into the project skill root so
// the name resolves; Path (where it lives on disk) is a read-it-yourself
// fallback. resolveSkills only reports what is on disk right now; init does the
// loading, see bindSkills.
func resolveSkills(node *graph.Node) []protocol.SkillRef {
	var out []protocol.SkillRef
	seen := map[string]bool{}

	add := func(ref protocol.SkillRef) {
		if ref.Name == "" || seen[ref.Name] {
			return
		}
		seen[ref.Name] = true
		out = append(out, ref)
	}

	// 1. auto-discover node-local skills: <node.Dir>/skills/<name>/SKILL.md
	for _, dir := range nodeSkillDirs(node) {
		add(refForDir(dir, srcNode))
	}

	// 2. explicitly declared skills: a path or a bare name
	for _, s := range node.Skills {
		add(resolveSkillRef(s, node.Dir))
	}
	return out
}

// nodeSkillDirs lists the skill directories that ship inside a node package.
func nodeSkillDirs(node *graph.Node) []string {
	if node.Dir == "" {
		return nil
	}
	root := filepath.Join(node.Dir, "skills")
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var dirs []string
	for _, e := range entries {
		// Follow symlinked skill directories the way the host does.
		if !e.IsDir() && e.Type()&os.ModeSymlink == 0 {
			continue
		}
		dir := filepath.Join(root, e.Name())
		if fileExists(filepath.Join(dir, "SKILL.md")) {
			dirs = append(dirs, dir)
		}
	}
	return dirs
}

// resolveSkillRef resolves one `skills:` entry. Entries are paths (to a skill
// directory or straight to its SKILL.md, relative to the node package) or bare
// names already known to the host.
func resolveSkillRef(s, nodeDir string) protocol.SkillRef {
	if isPathRef(s) {
		p := s
		if !filepath.IsAbs(p) && nodeDir != "" {
			p = filepath.Join(nodeDir, p)
		}
		abs, _ := filepath.Abs(skillDirOf(p))
		return refForDir(abs, srcPath)
	}
	// A bare name: the node's own copy first, then the host's skill roots,
	// then graphgine's.
	for _, r := range skillRoots(nodeDir) {
		dir := filepath.Join(r.dir, s)
		if fileExists(filepath.Join(dir, "SKILL.md")) {
			abs, _ := filepath.Abs(dir)
			return refForDir(abs, r.source)
		}
	}
	// Not on disk. The host may still have it registered (a plugin skill, say),
	// so binding by name is the only — and the right — thing left to try.
	return protocol.SkillRef{Name: s, Source: srcUnknown}
}

// refForDir describes a skill directory. The bind name is the directory name:
// that is what the host registers a skill under, whatever its frontmatter says.
func refForDir(dir, source string) protocol.SkillRef {
	name := filepath.Base(dir)
	return protocol.SkillRef{
		Name:   name,
		Path:   fileOrEmpty(filepath.Join(dir, "SKILL.md")),
		Source: source,
	}
}

// isPathRef reports whether a `skills:` entry is a path rather than a name.
func isPathRef(s string) bool {
	return filepath.IsAbs(s) || strings.ContainsRune(s, filepath.Separator) ||
		strings.Contains(s, "/") || strings.HasSuffix(s, ".md")
}

// skillDirOf normalizes a skill path: either the skill directory itself or the
// SKILL.md inside it.
func skillDirOf(p string) string {
	if strings.HasSuffix(p, ".md") {
		return filepath.Dir(p)
	}
	return p
}

// skillRoots is the search path for a bare skill name, most specific first.
func skillRoots(nodeDir string) []skillRoot {
	var roots []skillRoot
	if nodeDir != "" {
		roots = append(roots, skillRoot{filepath.Join(nodeDir, "skills"), srcNode})
	}
	roots = append(roots, hostSkillRoots()...)
	if g := os.Getenv("GRAPHGINE_SKILLS_DIR"); g != "" {
		roots = append(roots, skillRoot{g, srcSkillsDir})
	}
	return append(roots, skillRoot{"skills", srcSkillsDir})
}

// hostSkillRoots are the directories the host agent scans for skills. A skill
// that lives in one of these — or that a link in one of these points at — is
// addressable by name, and can therefore be bound to a sub-agent.
func hostSkillRoots() []skillRoot {
	var roots []skillRoot
	if wd, err := os.Getwd(); err == nil {
		roots = append(roots, skillRoot{filepath.Join(wd, ".claude", "skills"), srcProject})
	}
	for _, env := range []string{"CLAUDE_CONFIG_DIR", "CLAUDE_HOME"} {
		if v := os.Getenv(env); v != "" {
			roots = append(roots, skillRoot{filepath.Join(v, "skills"), srcUser})
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		roots = append(roots, skillRoot{filepath.Join(home, ".claude", "skills"), srcUser})
	}
	return roots
}

// sameDir reports whether two paths name the same directory, following links.
func sameDir(a, b string) bool {
	ra, err := filepath.EvalSymlinks(a)
	if err != nil {
		return false
	}
	rb, err := filepath.EvalSymlinks(b)
	if err != nil {
		return false
	}
	return ra == rb
}

func fileExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}

func fileOrEmpty(p string) string {
	if fileExists(p) {
		return p
	}
	return ""
}
