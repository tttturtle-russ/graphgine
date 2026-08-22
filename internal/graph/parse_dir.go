package graph

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// A graph package is a directory:
//
//	<graph-dir>/
//	  graph.yaml            # { name: ..., nodes: [ ... ] }
//	  nodes/
//	    <node-id>/
//	      node.yaml         # the Node definition (id defaults to the dir name)
//	      prompt.md         # optional; overrides node.yaml `prompt`
//	      skills/           # optional; skills bound to this node's sub-agent,
//	                        # travelling with it (node.yaml may also point
//	                        # `skills:` at a path or a host-known name)
//	        <skill-name>/SKILL.md
//
// There are two ways a package declares its topology:
//
//   - Explicit topology (preferred): graph.yaml lists the nodes and their
//     dependency edges. Each entry names a node whose *behavior* is defined in
//     nodes/<id>/node.yaml; the *structure* (which nodes, and the deps between
//     them) lives in graph.yaml. In this mode node.yaml must NOT declare deps —
//     graph.yaml is the single source of truth for the DAG shape, e.g.
//
//	name: my-graph
//	nodes:
//	  - id: recon
//	  - id: analyze
//	    deps: [recon]
//
//   - Auto-discovery (legacy): graph.yaml carries only `name`, every
//     nodes/<id>/ with a node.yaml becomes a node, and each node.yaml declares
//     its own deps. Kept for backward compatibility.
//
// The node's directory name is its id (node.yaml may override with `id:` only in
// auto-discovery mode; in explicit mode the graph.yaml id wins).

type graphMeta struct {
	Name  string          `yaml:"name"`
	Nodes []manifestEntry `yaml:"nodes"`
}

// manifestEntry is one node listed in graph.yaml's explicit-topology `nodes:`.
// It carries only structure — the node id and its dependency edges; the node's
// behavior lives in nodes/<id>/node.yaml. It accepts either a bare string
// (`- recon`, a node with no deps) or a mapping (`- {id: recon, deps: [...]}`).
type manifestEntry struct {
	ID   string   `yaml:"id"`
	Deps []string `yaml:"deps,omitempty"`
}

func (m *manifestEntry) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.ScalarNode {
		return value.Decode(&m.ID)
	}
	type raw manifestEntry // avoid recursing into this method
	var r raw
	if err := value.Decode(&r); err != nil {
		return err
	}
	*m = manifestEntry(r)
	return nil
}

// ParseDir loads a node-package graph from a directory. It does not validate
// semantics; call Validate for that.
func ParseDir(dir string) (*Graph, error) {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	g := &Graph{}

	var meta graphMeta
	if data, err := os.ReadFile(filepath.Join(absDir, "graph.yaml")); err == nil {
		if err := yaml.Unmarshal(data, &meta); err != nil {
			return nil, fmt.Errorf("parse graph.yaml: %w", err)
		}
		g.Name = meta.Name
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if g.Name == "" {
		g.Name = filepath.Base(absDir)
	}

	nodesDir := filepath.Join(absDir, "nodes")
	if len(meta.Nodes) > 0 {
		g.Nodes, err = loadExplicit(nodesDir, meta.Nodes)
	} else {
		g.Nodes, err = loadAutoDiscover(nodesDir)
	}
	if err != nil {
		return nil, err
	}
	if len(g.Nodes) == 0 {
		return nil, fmt.Errorf("no nodes found under %s", nodesDir)
	}
	return g, nil
}

// loadExplicit builds the node list from graph.yaml's explicit `nodes:`
// manifest: structure (id + deps) comes from the manifest, behavior from each
// nodes/<id>/node.yaml. Declaration order is preserved.
func loadExplicit(nodesDir string, entries []manifestEntry) ([]*Node, error) {
	var nodes []*Node
	seen := map[string]bool{}
	for _, e := range entries {
		id := strings.TrimSpace(e.ID)
		if id == "" {
			return nil, fmt.Errorf("graph.yaml: a nodes entry has an empty id")
		}
		if seen[id] {
			return nil, fmt.Errorf("graph.yaml: duplicate node id %q", id)
		}
		seen[id] = true

		ndir := filepath.Join(nodesDir, id)
		if _, err := os.Stat(filepath.Join(ndir, "node.yaml")); err != nil {
			return nil, fmt.Errorf("node %q: listed in graph.yaml but nodes/%s/node.yaml is missing", id, id)
		}
		n, err := loadNodeDir(ndir)
		if err != nil {
			return nil, fmt.Errorf("node %q: %w", id, err)
		}
		if len(n.Deps) > 0 {
			return nil, fmt.Errorf("node %q: deps must be declared in graph.yaml, not in node.yaml (explicit-topology package)", id)
		}
		if n.ID != "" && n.ID != id {
			return nil, fmt.Errorf("node %q: node.yaml sets id %q, which disagrees with graph.yaml", id, n.ID)
		}
		n.ID = id
		n.Deps = e.Deps
		nodes = append(nodes, n)
	}
	return nodes, nil
}

// loadAutoDiscover builds the node list by scanning nodes/ for subdirectories
// that contain a node.yaml (legacy mode). Each node.yaml declares its own deps;
// nodes are returned sorted by id for determinism.
func loadAutoDiscover(nodesDir string) ([]*Node, error) {
	entries, err := os.ReadDir(nodesDir)
	if err != nil {
		return nil, fmt.Errorf("read nodes dir: %w", err)
	}
	var nodes []*Node
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		ndir := filepath.Join(nodesDir, e.Name())
		if _, err := os.Stat(filepath.Join(ndir, "node.yaml")); err != nil {
			continue // a nodes/ subdir without node.yaml is not a node
		}
		n, err := loadNodeDir(ndir)
		if err != nil {
			return nil, fmt.Errorf("node %q: %w", e.Name(), err)
		}
		if n.ID == "" {
			n.ID = e.Name()
		}
		nodes = append(nodes, n)
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].ID < nodes[j].ID })
	return nodes, nil
}

// loadNodeDir loads one node package directory: node.yaml plus an optional
// prompt.md that overrides node.yaml's `prompt`. It sets Dir; the caller owns
// ID and Deps (which differ between explicit and auto-discovery modes).
func loadNodeDir(ndir string) (*Node, error) {
	n, err := parseNodeFile(filepath.Join(ndir, "node.yaml"))
	if err != nil {
		return nil, err
	}
	n.Dir = ndir
	if p, err := os.ReadFile(filepath.Join(ndir, "prompt.md")); err == nil {
		n.Prompt = strings.TrimRight(string(p), "\n")
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	return n, nil
}

func parseNodeFile(path string) (*Node, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var n Node
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&n); err != nil {
		return nil, fmt.Errorf("parse node.yaml: %w", err)
	}
	return &n, nil
}

// Load parses a graph from either a single YAML file (inline format) or a graph
// package directory, dispatching on what the path points at.
func Load(path string) (*Graph, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return ParseDir(path)
	}
	return ParseFile(path)
}
