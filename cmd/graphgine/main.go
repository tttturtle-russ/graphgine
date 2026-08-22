// Command graphgine is the zero-LLM orchestration state machine that a driving
// main agent calls: init a run from a declared YAML graph, ask for the next
// agent/human work, report results, observe status, and finalize.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/tttturtle-russ/graphgine/internal/executor"
	"github.com/tttturtle-russ/graphgine/internal/runstore"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd := os.Args[1]
	args := os.Args[2:]

	var err error
	switch cmd {
	case "init":
		err = cmdInit(args)
	case "next":
		err = cmdNext(args)
	case "report":
		err = cmdReport(args)
	case "status":
		err = cmdStatus(args)
	case "finalize":
		err = cmdFinalize(args) // may os.Exit with the run's exit code
	case "-h", "--help", "help":
		usage()
		return
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", cmd)
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func runsFlag(fs *flag.FlagSet) *string {
	def := os.Getenv("GRAPHGINE_RUNS")
	if def == "" {
		def = "runs"
	}
	return fs.String("runs", def, "runs directory (or $GRAPHGINE_RUNS)")
}

// parseArgs parses flags that may be interspersed with positional arguments
// (Go's flag package otherwise stops at the first positional), returning the
// collected positionals in order.
func parseArgs(fs *flag.FlagSet, args []string) []string {
	var positionals []string
	for {
		fs.Parse(args)
		if fs.NArg() == 0 {
			break
		}
		positionals = append(positionals, fs.Arg(0))
		args = fs.Args()[1:]
	}
	return positionals
}

func cmdInit(args []string) error {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	runs := runsFlag(fs)
	concurrency := fs.Int("concurrency", 10, "max nodes executed concurrently")
	skillsDir := fs.String("skills-dir", os.Getenv("GRAPHGINE_SKILLS_LINK_DIR"),
		"host skill root to register the graph's skills into (or $GRAPHGINE_SKILLS_LINK_DIR)")
	pos := parseArgs(fs, args)
	if len(pos) != 1 {
		return fmt.Errorf("usage: graphgine init <graph.yaml> [--concurrency N] [--runs DIR] [--skills-dir DIR]")
	}
	res, err := executor.Init(runstore.NewStore(*runs), pos[0], executor.InitOptions{
		Concurrency: *concurrency,
		SkillsDir:   *skillsDir,
	})
	if err != nil {
		return err
	}
	return emit(res)
}

func cmdNext(args []string) error {
	fs := flag.NewFlagSet("next", flag.ExitOnError)
	runs := runsFlag(fs)
	pos := parseArgs(fs, args)
	if len(pos) != 1 {
		return fmt.Errorf("usage: graphgine next <run_id> [--runs DIR]")
	}
	run, err := runstore.NewStore(*runs).Open(pos[0])
	if err != nil {
		return err
	}
	defer run.Close()
	return emit(executor.Next(run))
}

func cmdReport(args []string) error {
	fs := flag.NewFlagSet("report", flag.ExitOnError)
	runs := runsFlag(fs)
	status := fs.String("status", "", "success|failed (required)")
	output := fs.String("output", "", "path to a JSON output file (optional)")
	pos := parseArgs(fs, args)
	if len(pos) != 2 || *status == "" {
		return fmt.Errorf("usage: graphgine report <run_id> <node_id> --status success|failed [--output FILE] [--runs DIR]")
	}
	run, err := runstore.NewStore(*runs).Open(pos[0])
	if err != nil {
		return err
	}
	defer run.Close()
	res, err := executor.Report(run, pos[1], *status, *output)
	if err != nil {
		return err
	}
	return emit(res)
}

func cmdStatus(args []string) error {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	runs := runsFlag(fs)
	pos := parseArgs(fs, args)
	if len(pos) != 1 {
		return fmt.Errorf("usage: graphgine status <run_id> [--runs DIR]")
	}
	run, err := runstore.NewStore(*runs).Open(pos[0])
	if err != nil {
		return err
	}
	defer run.Close()
	return emit(executor.Status(run))
}

func cmdFinalize(args []string) error {
	fs := flag.NewFlagSet("finalize", flag.ExitOnError)
	runs := runsFlag(fs)
	pos := parseArgs(fs, args)
	if len(pos) != 1 {
		return fmt.Errorf("usage: graphgine finalize <run_id> [--runs DIR]")
	}
	run, err := runstore.NewStore(*runs).Open(pos[0])
	if err != nil {
		return err
	}
	res := executor.Finalize(run)
	run.Close()
	if err := emit(res); err != nil {
		return err
	}
	os.Exit(res.ExitCode)
	return nil
}

func emit(v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(data))
	return nil
}

func usage() {
	fmt.Fprint(os.Stderr, `graphgine — declarative graph orchestration state machine

usage:
  graphgine init <graph.yaml> [--concurrency N] [--runs DIR] [--skills-dir DIR]
  graphgine next <run_id> [--runs DIR]
  graphgine report <run_id> <node_id> --status success|failed [--output FILE] [--runs DIR]
  graphgine status <run_id> [--runs DIR]
  graphgine finalize <run_id> [--runs DIR]

The main agent drives the loop: init -> (next -> execute agent/human work -> report)* -> finalize.
Command nodes are executed directly by graphgine; agent/human nodes are handed back via 'next'.
'init' reads each agent node's skills, registers them with the host, and records the bindings;
'next' hands them back as bindings the driver attaches to the sub-agent, never as text.
`)
}
