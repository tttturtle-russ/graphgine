// Package runstore persists a run's state machine to disk and manages the
// per-node workspaces. The CLI is a zero-LLM, fully persisted state machine:
// because state.json + workspaces survive across invocations, a run is
// resumable and observable at any point.
package runstore

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/tttturtle-russ/graphgine/internal/graph"
	"github.com/tttturtle-russ/graphgine/internal/protocol"
)

// Status is a node instance's lifecycle state.
type Status string

const (
	Pending Status = "pending" // deps not yet satisfied, or awaiting expansion
	Running Status = "running" // command executing / agent-human dispatched / scatter gathering
	Success Status = "success"
	Failed  Status = "failed"
	Skipped Status = "skipped" // a required dependency failed or was skipped
)

// Terminal reports whether a status is final (won't change without a retry).
func (s Status) Terminal() bool { return s == Success || s == Failed || s == Skipped }

// NodeState is the mutable per-instance state. Instances of a scatter node get
// ids like "analyze[3]"; every other node's instance id equals its base id.
type NodeState struct {
	ID       string         `json:"id"`
	BaseID   string         `json:"base_id"`
	Type     graph.NodeType `json:"type"`
	Status   Status         `json:"status"`
	Attempts int            `json:"attempts"`
	Fatal    bool           `json:"fatal"`
	Deps     []string       `json:"deps"`          // base ids
	Optional []string       `json:"optional_deps"` // subset of Deps that may be absent
	Message  string         `json:"message,omitempty"`

	// scatter template bookkeeping
	IsScatterTemplate bool     `json:"is_scatter_template,omitempty"`
	Expanded          bool     `json:"expanded,omitempty"`
	Instances         []string `json:"instances,omitempty"`

	// scatter instance bookkeeping
	Index int `json:"index,omitempty"`
}

// State is the full persisted run state.
type State struct {
	ID          string                `json:"id"`
	GraphName   string                `json:"graph_name"`
	Concurrency int                   `json:"concurrency"`
	CreatedAt   string                `json:"created_at"`
	Failed      bool                  `json:"failed"` // latched task-level fatal failure
	Order       []string              `json:"order"`  // deterministic iteration order of instance ids
	Nodes       map[string]*NodeState `json:"nodes"`
	Graph       *graph.Graph          `json:"graph"`

	// Skills are the skill bindings init recorded for each agent node (by base
	// id): which skill the node's sub-agent gets and how it is attached. They
	// are decided once, when init registers the graph's skills with the host,
	// and replayed at every spawn — including after a resume.
	Skills map[string][]protocol.SkillRef `json:"skills,omitempty"`

	// SkillLinks are the symlinks init created into the project skill root for
	// this run. finalize removes the ones this run created (see cleanupLinks),
	// so the graph's bundled skills do not outlive the run.
	SkillLinks []protocol.SkillLink `json:"skill_links,omitempty"`
}

// Run couples persisted state with its on-disk location and a store handle.
type Run struct {
	State *State
	Dir   string
	mu    sync.Mutex
	lockF *os.File
}

// Store roots all runs under a directory.
type Store struct{ Root string }

// NewStore returns a store rooted at root (created lazily).
func NewStore(root string) *Store { return &Store{Root: root} }

// Create initializes a new run from a validated graph and lays out workspaces.
func (s *Store) Create(g *graph.Graph, concurrency int) (*Run, error) {
	if concurrency < 1 {
		concurrency = 1
	}
	id, err := newRunID(g.Name)
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(s.Root, id)
	if err := os.MkdirAll(filepath.Join(dir, "nodes"), 0o755); err != nil {
		return nil, err
	}

	st := &State{
		ID:          id,
		GraphName:   g.Name,
		Concurrency: concurrency,
		CreatedAt:   time.Now().UTC().Format(time.RFC3339),
		Nodes:       map[string]*NodeState{},
		Graph:       g,
	}
	for _, n := range g.Nodes {
		ns := &NodeState{
			ID: n.ID, BaseID: n.ID, Type: n.Type, Status: Pending,
			Fatal: n.Fatal, Deps: append([]string{}, n.Deps...),
			Optional: append([]string{}, n.OptionalInputs...),
			Index:    -1,
		}
		if n.IsScatter() {
			ns.IsScatterTemplate = true
		}
		st.Nodes[n.ID] = ns
		st.Order = append(st.Order, n.ID)
	}

	r := &Run{State: st, Dir: dir}
	for _, n := range g.Nodes {
		if err := r.ensureWorkspace(n.ID); err != nil {
			return nil, err
		}
	}
	if err := r.save(); err != nil {
		return nil, err
	}
	return r, nil
}

// Open loads a run and acquires an exclusive lock, serializing CLI invocations
// against the same run. Call Close when done.
func (s *Store) Open(runID string) (*Run, error) {
	dir := filepath.Join(s.Root, runID)
	lf, err := os.OpenFile(filepath.Join(dir, "lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open run %q: %w", runID, err)
	}
	if err := syscall.Flock(int(lf.Fd()), syscall.LOCK_EX); err != nil {
		lf.Close()
		return nil, fmt.Errorf("lock run %q: %w", runID, err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if err != nil {
		syscall.Flock(int(lf.Fd()), syscall.LOCK_UN)
		lf.Close()
		return nil, fmt.Errorf("read state for %q: %w", runID, err)
	}
	var st State
	if err := json.Unmarshal(data, &st); err != nil {
		syscall.Flock(int(lf.Fd()), syscall.LOCK_UN)
		lf.Close()
		return nil, fmt.Errorf("parse state for %q: %w", runID, err)
	}
	return &Run{State: &st, Dir: dir, lockF: lf}, nil
}

// Close persists state and releases the run lock.
func (r *Run) Close() error {
	err := r.Save()
	if r.lockF != nil {
		syscall.Flock(int(r.lockF.Fd()), syscall.LOCK_UN)
		r.lockF.Close()
		r.lockF = nil
	}
	return err
}

// Lock/Unlock guard in-process concurrent state mutation (parallel command execs).
func (r *Run) Lock()   { r.mu.Lock() }
func (r *Run) Unlock() { r.mu.Unlock() }

// Save persists state.json atomically.
func (r *Run) Save() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.save()
}

func (r *Run) save() error {
	data, err := json.MarshalIndent(r.State, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(r.Dir, "state.json.tmp")
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(r.Dir, "state.json"))
}

// --- workspace paths ---

// NodeDir is <run>/nodes/<instance_id>.
func (r *Run) NodeDir(id string) string { return filepath.Join(r.Dir, "nodes", id) }

// InputDir / OutputDir / LogsDir for a node instance.
func (r *Run) InputDir(id string) string  { return filepath.Join(r.NodeDir(id), "input") }
func (r *Run) OutputDir(id string) string { return filepath.Join(r.NodeDir(id), "output") }
func (r *Run) LogsDir(id string) string   { return filepath.Join(r.NodeDir(id), "logs") }

// OutputFile is where a node's structured output.json lives.
func (r *Run) OutputFile(id string) string { return filepath.Join(r.OutputDir(id), "output.json") }

func (r *Run) ensureWorkspace(id string) error {
	for _, d := range []string{r.InputDir(id), r.OutputDir(id), r.LogsDir(id)} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	return nil
}

// EnsureWorkspace exposes workspace creation for runtime-created scatter instances.
func (r *Run) EnsureWorkspace(id string) error { return r.ensureWorkspace(id) }

// ReadOutput loads a node's structured output.json (nil if absent).
func (r *Run) ReadOutput(id string) (any, error) {
	data, err := os.ReadFile(r.OutputFile(id))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return nil, fmt.Errorf("output of %q is not valid json: %w", id, err)
	}
	return v, nil
}

// WriteOutput stores a node's structured output.json.
func (r *Run) WriteOutput(id string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(r.OutputFile(id), data, 0o644)
}

func newRunID(name string) (string, error) {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	base := sanitize(name)
	if base == "" {
		base = "run"
	}
	return fmt.Sprintf("%s-%s-%s", base, time.Now().UTC().Format("20060102-150405"), hex.EncodeToString(b[:])), nil
}

func sanitize(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ' || r == '_' || r == '-':
			b.WriteRune('-')
		}
	}
	return strings.Trim(b.String(), "-")
}
