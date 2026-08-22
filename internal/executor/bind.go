package executor

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/tttturtle-russ/graphgine/internal/graph"
	"github.com/tttturtle-russ/graphgine/internal/protocol"
)

// Link statuses reported in protocol.SkillLink.Status.
const (
	linkCreated    = "linked"
	linkRegistered = "registered"
	linkConflict   = "conflict"
	linkFailed     = "failed"
)

// defaultSkillsDir is the host skill root init registers a graph's skills into.
// It defaults to the *project-level* root (<cwd>/.claude/skills) so a graph's
// bundled skills stay scoped to the project the run happens in — not linked into
// the user's global ~/.claude, where they would outlive the run and collide with
// other projects. finalize removes the links this run created (see cleanupLinks).
// $GRAPHGINE_SKILLS_LINK_DIR (or `init --skills-dir`) overrides the location.
func defaultSkillsDir() string {
	if d := os.Getenv("GRAPHGINE_SKILLS_LINK_DIR"); d != "" {
		return d
	}
	if wd, err := os.Getwd(); err == nil {
		return filepath.Join(wd, ".claude", "skills")
	}
	return filepath.Join(".claude", "skills")
}

// bindSkills is init's skill pass: for every agent node it reads the node's
// skill directory, registers what it finds with the host, and records how the
// node's sub-agent will attach each skill. The recorded bindings are what every
// later spawn uses — resolution happens once, at init.
//
// Registering means linking the skill into a directory the host scans, under
// its own directory name. A skill that ships inside a graph lives where the
// host never looks; the link gives it a name to bind, and the skill itself
// stays in the graph. Names already taken are left alone.
func bindSkills(g *graph.Graph, dir string) (map[string][]protocol.SkillRef, []protocol.SkillLink) {
	if dir == "" {
		dir = defaultSkillsDir()
	}
	bindings := map[string][]protocol.SkillRef{}
	var links []protocol.SkillLink
	done := map[string]protocol.SkillLink{} // skill name -> its registration

	for _, n := range g.Nodes {
		if n.Type != graph.NodeAgent {
			continue
		}
		refs := resolveSkills(n)
		for _, ref := range refs {
			if ref.Path == "" {
				continue // no file to register; the host may know the name already
			}
			src := filepath.Dir(ref.Path)
			link, seen := done[ref.Name]
			if !seen {
				link = registerSkill(src, ref.Name, dir)
				done[ref.Name] = link
				links = append(links, link)
			} else if link.Path != src {
				links = append(links, protocol.SkillLink{
					Name: ref.Name, Path: src, Status: linkConflict,
					Detail: fmt.Sprintf("another node registers %q from %s", ref.Name, link.Path),
				})
			}
		}
		if len(refs) > 0 {
			bindings[n.ID] = refs
		}
	}
	sort.Slice(links, func(i, j int) bool { return links[i].Name < links[j].Name })
	return bindings, links
}

// registerSkill points <root>/<name> at the skill directory src.
func registerSkill(src, name, root string) protocol.SkillLink {
	link := filepath.Join(root, name)
	l := protocol.SkillLink{Name: name, Path: src, Link: link}

	switch info, err := os.Lstat(link); {
	case err == nil && sameDir(link, src):
		l.Status = linkRegistered
		return l
	case err == nil && info.Mode()&os.ModeSymlink != 0 && !dirExists(link):
		// A link left behind by a graph that has since moved. It registers
		// nothing and holds the name hostage, so replace it.
		if err := os.Remove(link); err != nil {
			return failedLink(l, err)
		}
		l.Detail = "replaced a broken link"
	case err == nil:
		l.Status = linkConflict
		l.Detail = fmt.Sprintf("%s already exists and is not this skill", link)
		return l
	case !os.IsNotExist(err):
		return failedLink(l, err)
	}

	if err := os.MkdirAll(root, 0o755); err != nil {
		return failedLink(l, err)
	}
	if err := os.Symlink(src, link); err != nil {
		return failedLink(l, err)
	}
	l.Status = linkCreated
	return l
}

func failedLink(l protocol.SkillLink, err error) protocol.SkillLink {
	l.Status = linkFailed
	l.Detail = err.Error()
	return l
}

// linkMessage explains what a registration round means for the run about to
// start. Links go into the project skill root for the duration of the run and
// are removed on finalize; a freshly linked skill is not bindable by name in
// this session yet (the host scans its skill roots when a session starts), so
// sub-agents read the skill by its path this run.
func linkMessage(links []protocol.SkillLink) string {
	var created, failed []string
	for _, l := range links {
		switch l.Status {
		case linkCreated:
			created = append(created, l.Name)
		case linkConflict, linkFailed:
			failed = append(failed, l.Name)
		}
	}
	var msg string
	if len(created) > 0 {
		msg = fmt.Sprintf("linked %d graph skill(s) into the project skill root for this run: %v. "+
			"Sub-agents read each skill by its path; the links are removed on finalize.", len(created), created)
	}
	if len(failed) > 0 {
		if msg != "" {
			msg += " "
		}
		msg += fmt.Sprintf("could not register %v (see skills[].detail); sub-agents read those by path.", failed)
	}
	return msg
}

// cleanupLinks removes the skill links init created for this run — init's link
// pass is per-run, so finalize hands the links back to be undone. Only links
// this run *created* are touched, and only while the symlink still points at the
// skill we recorded: a name since taken over by something else, or a link a
// concurrent run already removed, is left alone. Returns the names unlinked.
func cleanupLinks(links []protocol.SkillLink) []string {
	var removed []string
	for _, l := range links {
		if l.Status != linkCreated || l.Link == "" {
			continue
		}
		fi, err := os.Lstat(l.Link)
		if err != nil || fi.Mode()&os.ModeSymlink == 0 {
			continue // gone already, or not the symlink we made
		}
		if !sameDir(l.Link, l.Path) {
			continue // repointed since — not ours to remove
		}
		if err := os.Remove(l.Link); err == nil {
			removed = append(removed, l.Name)
		}
	}
	sort.Strings(removed)
	return removed
}

func dirExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}
