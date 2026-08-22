package executor

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/tttturtle-russ/graphgine/internal/graph"
	"github.com/tttturtle-russ/graphgine/internal/runstore"
)

// prepareInputs materializes each dependency's declared output into this node's
// isolated input directory (input/<dep>/...), so the node sees exactly — and
// only — the outputs of its declared deps. This keeps every node's input set
// fully reproducible. A dependency listed in optional_inputs that did not
// succeed is represented by an ABSENT marker instead of failing the node.
func prepareInputs(r *runstore.Run, ns *runstore.NodeState, node *graph.Node) error {
	for _, d := range node.Deps {
		dep := r.State.Nodes[d]
		destDir := filepath.Join(r.InputDir(ns.ID), d)
		if err := os.RemoveAll(destDir); err != nil {
			return err
		}
		if err := os.MkdirAll(destDir, 0o755); err != nil {
			return err
		}
		if dep != nil && dep.Status == runstore.Success {
			if err := copyTree(r.OutputDir(d), destDir); err != nil {
				return err
			}
		} else {
			// optional dep that is absent (the only way we reach here for a
			// runnable node — a failed required dep would have skipped us).
			if err := os.WriteFile(filepath.Join(destDir, "ABSENT"), nil, 0o644); err != nil {
				return err
			}
		}
	}
	return nil
}

// resolveInput builds a scatter instance's resolved input map, substituting
// ${item} / ${index} and any ${node.output.field} references.
func resolveInput(r *runstore.Run, ns *runstore.NodeState, node *graph.Node) (map[string]any, error) {
	item, err := readItem(r, ns.ID)
	if err != nil {
		return nil, err
	}
	out := make(map[string]any, len(node.Input))
	for k, v := range node.Input {
		out[k] = resolveValue(r, v, item, ns.Index)
	}
	return out, nil
}

func resolveValue(r *runstore.Run, v any, item any, index int) any {
	s, ok := v.(string)
	if !ok {
		return v
	}
	switch s {
	case "${item}":
		return item
	case "${index}":
		return index
	}
	if ref, isRef, err := graph.ParseRef(s); isRef && err == nil {
		if out, e := r.ReadOutput(ref.NodeID); e == nil {
			if nav, e2 := navigate(out, ref.Path); e2 == nil {
				return nav
			}
		}
	}
	return s
}

func writeItem(r *runstore.Run, id string, item any) error {
	data, err := json.MarshalIndent(item, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(r.InputDir(id), "item.json"), data, 0o644)
}

func readItem(r *runstore.Run, id string) (any, error) {
	data, err := os.ReadFile(filepath.Join(r.InputDir(id), "item.json"))
	if err != nil {
		return nil, err
	}
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return nil, err
	}
	return v, nil
}

// navigate walks a decoded JSON value along a field path (objects only).
func navigate(v any, path []string) (any, error) {
	cur := v
	for _, p := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("cannot descend into %q: not an object", p)
		}
		cur, ok = m[p]
		if !ok {
			return nil, fmt.Errorf("field %q not found", p)
		}
	}
	return cur, nil
}

// copyTree recursively copies the contents of src into dst. A missing src is a
// no-op (an upstream may legitimately produce no files beyond output.json).
func copyTree(src, dst string) error {
	info, err := os.Stat(src)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return copyFile(src, dst)
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	for _, e := range entries {
		s := filepath.Join(src, e.Name())
		d := filepath.Join(dst, e.Name())
		if e.IsDir() {
			if err := copyTree(s, d); err != nil {
				return err
			}
		} else {
			if err := copyFile(s, d); err != nil {
				return err
			}
		}
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return nil
}
