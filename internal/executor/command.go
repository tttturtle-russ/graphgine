package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tttturtle-russ/graphgine/internal/graph"
	"github.com/tttturtle-russ/graphgine/internal/runstore"
	"github.com/tttturtle-russ/graphgine/internal/schema"
)

type cmdResult struct {
	ns     *runstore.NodeState
	exit   int
	err    error
	output any
}

// execCommandBatch runs a batch of runnable command nodes concurrently (the
// batch size is already bounded by the concurrency slots), then applies their
// outcomes to the state under the caller's assumed single-threaded control.
func execCommandBatch(r *runstore.Run, batch []*runstore.NodeState) {
	for _, ns := range batch {
		ns.Status = runstore.Running
	}
	results := make([]cmdResult, len(batch))
	var wg sync.WaitGroup
	for i, ns := range batch {
		wg.Add(1)
		go func(i int, ns *runstore.NodeState) {
			defer wg.Done()
			results[i] = runCommand(r, ns)
		}(i, ns)
	}
	wg.Wait()

	for _, rr := range results {
		node := r.State.Graph.Node(rr.ns.BaseID)
		if rr.err != nil || rr.exit != 0 {
			handleFailure(r, rr.ns, node, fmt.Sprintf("command failed (exit=%d): %v", rr.exit, rr.err))
			continue
		}
		if errs := schema.Validate(node.OutputSchema, rr.output); len(errs) > 0 {
			handleFailure(r, rr.ns, node, "command output failed schema validation: "+joinErrs(errs))
			continue
		}
		if err := r.WriteOutput(rr.ns.ID, rr.output); err != nil {
			handleFailure(r, rr.ns, node, "persist output: "+err.Error())
			continue
		}
		rr.ns.Status = runstore.Success
		rr.ns.Message = ""
	}
}

func runCommand(r *runstore.Run, ns *runstore.NodeState) cmdResult {
	node := r.State.Graph.Node(ns.BaseID)
	if err := prepareInputs(r, ns, node); err != nil {
		return cmdResult{ns: ns, exit: -1, err: fmt.Errorf("prepare inputs: %w", err)}
	}

	ctx := context.Background()
	if node.Timeout != "" {
		if d, err := time.ParseDuration(node.Timeout); err == nil {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, d)
			defer cancel()
		}
	}

	cmd := exec.CommandContext(ctx, node.Command[0], node.Command[1:]...)
	cmd.Dir = r.NodeDir(ns.ID)
	cmd.Env = commandEnv(r, ns, node)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()

	_ = os.WriteFile(filepath.Join(r.LogsDir(ns.ID), "stdout.log"), stdout.Bytes(), 0o644)
	_ = os.WriteFile(filepath.Join(r.LogsDir(ns.ID), "stderr.log"), stderr.Bytes(), 0o644)

	exit := exitCode(runErr)
	if runErr != nil && exit == 0 {
		exit = -1 // failed to start / context error
	}
	return cmdResult{ns: ns, exit: exit, err: nilIfExit(runErr), output: commandOutput(r, ns, &stdout, exit)}
}

// commandOutput derives the node's structured output: prefer an output.json the
// command wrote itself, else the command's stdout parsed as JSON, else a
// wrapper carrying the raw stdout.
func commandOutput(r *runstore.Run, ns *runstore.NodeState, stdout *bytes.Buffer, exit int) any {
	if _, err := os.Stat(r.OutputFile(ns.ID)); err == nil {
		if out, err := r.ReadOutput(ns.ID); err == nil && out != nil {
			return out
		}
	}
	trimmed := strings.TrimSpace(stdout.String())
	if trimmed != "" && json.Valid([]byte(trimmed)) {
		var v any
		if err := json.Unmarshal([]byte(trimmed), &v); err == nil {
			return v
		}
	}
	return map[string]any{"stdout": stdout.String(), "exit_code": exit}
}

func commandEnv(r *runstore.Run, ns *runstore.NodeState, node *graph.Node) []string {
	env := os.Environ()
	env = append(env,
		"GRAPHGINE_RUN_DIR="+r.Dir,
		"GRAPHGINE_NODE_ID="+ns.ID,
		"GRAPHGINE_INPUT_DIR="+r.InputDir(ns.ID),
		"GRAPHGINE_OUTPUT_DIR="+r.OutputDir(ns.ID),
		"GRAPHGINE_OUTPUT_FILE="+r.OutputFile(ns.ID),
	)
	for k, v := range node.Env {
		env = append(env, k+"="+v)
	}
	if ns.Index >= 0 { // scatter instance
		env = append(env, "GRAPHGINE_INDEX="+strconv.Itoa(ns.Index))
		if item, err := readItem(r, ns.ID); err == nil {
			if data, err := json.Marshal(item); err == nil {
				env = append(env, "GRAPHGINE_ITEM="+string(data))
			}
		}
	}
	return env
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if ok := errors.As(err, &ee); ok {
		return ee.ExitCode()
	}
	return -1
}

// nilIfExit returns nil when the error is purely a non-zero exit (which we
// already capture via the exit code), and the original error otherwise.
func nilIfExit(err error) error {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return nil
	}
	return err
}

func joinErrs(errs []error) string {
	ss := make([]string, 0, len(errs))
	for _, e := range errs {
		ss = append(ss, e.Error())
	}
	return strings.Join(ss, "; ")
}
