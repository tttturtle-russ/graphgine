# graphgine

A **graph-engineering** orchestration base for building vulnerability-detection
(and other) workflows on top of coding agents like Claude Code.

You declare a static DAG of subtasks in YAML. `graphgine` — a **zero-LLM** Go
state machine — parses and validates the graph, runs the deterministic parts
itself, hands the intelligent parts to a driving *main agent*, validates every
node's output against a schema, and persists everything so a run is observable
and resumable. Domain knowledge (skills, commands, schemas) lives entirely in
the declared graph; the engine is domain-agnostic.

The north-star idea: make expert vulnerability-hunting **methodology** a
versionable, reusable, shareable graph asset — a "package manager for hunting
methodology" — rather than another general agent orchestrator.

## Node types

| type | who runs it | purpose |
|------|-------------|---------|
| `command` | **graphgine** (directly, no LLM) | deterministic tools/scripts |
| `agent` | the **main agent** (spawns an isolated sub-agent) | the intelligent subtasks |
| `human` | the **main agent** (solicits a person) | review / approval gates |

A graph with only `command` + `human` nodes runs with **zero LLM** — the CLI
drives it end to end.

## Install

```sh
./install.sh          # builds the binary -> ~/.local/bin, installs the driver
                      # skill -> ~/.claude/skills/graphgine, the sec-graph node
                      # worker -> ~/.claude/agents, and the bundled node skills
                      # -> ~/.claude/skills
./install.sh --uninstall
```
Flags: `--bin-dir`, `--claude-dir`, `--skills-dir`. After install, add
`~/.local/bin` to `PATH` (if needed). Bundled node skills go into the host's own
skills dir on purpose: that is what lets a node **bind** them to its sub-agent by
name instead of the driver pasting their text (see below). `--skills-dir` puts
them elsewhere — then `export GRAPHGINE_SKILLS_DIR=<that dir>` so the CLI can
still resolve them by path.

### Or build manually

```sh
go build -o bin/graphgine ./cmd/graphgine
go test ./...
```

## Quickstart (pure command + scatter, runs standalone)

```sh
RID=$(bin/graphgine init examples/pipeline/graph.yaml --concurrency 4 | sed -n 's/.*"run_id": "\(.*\)",/\1/p')
bin/graphgine next "$RID"        # drives every command node to completion
bin/graphgine status "$RID"      # observe the DAG state
bin/graphgine finalize "$RID"    # exit code reflects any fatal failure
```

## The node model (YAML)

```yaml
name: my-graph
nodes:
  - id: scan
    type: command
    deps: [recon]                 # dependency edges → the DAG
    fatal: false                  # if it fails (retries spent), does the whole task fail?
    retry: { max: 2, backoff: 5s }
    optional_inputs: [recon]      # a dep that may be absent without skipping this node
    command: ["sh", "-c", "..."]  # command nodes
    env: { KEY: value }
    timeout: 30s
    output_schema:                # JSON Schema (subset) — output is validated
      type: object
      required: [candidates]

  - id: analyze
    type: agent
    deps: [scan]
    foreach: ${scan.output.candidates}   # scatter: one instance per array element
    input: { candidate: "${item}" }      # ${item} / ${index} substituted per instance
    skills:                              # bound to this node's sub-agent
      - ./skills/sink-walk               #   a path — the skill travels with the node
      - memory-corruption-triage         #   a name the host already knows
    agent: sec-graph                     # optional: which sub-agent type to spawn
    tools: [read, bash]
    prompt: |
      Triage the candidate in your input ...
    output_schema: { type: object, required: [verdict] }   # describes ONE instance

  - id: review
    type: human
    deps: [analyze]                # receives the gathered survivor array
    prompt: "Approve or reject the findings."
    output_schema: { type: object, required: [decision] }
```

### Node packages (a folder per node)

Beyond the single-file form above, a graph can be a **directory** where each node
is its own folder — cleaner as nodes grow prompts and skills:

```
my-graph/
  graph.yaml                     # name + the explicit topology (nodes + edges)
  nodes/
    recon/
      node.yaml                  # type/output_schema/command/... — behavior only, no deps
    analyze/
      node.yaml                  # type: agent, foreach, input, tools, output_schema
      prompt.md                  # the sub-agent prompt (overrides node.yaml `prompt`)
      skills/
        memory-corruption-triage/
          SKILL.md               # bound to this node's sub-agent — the skill travels with the node
```

**graph.yaml owns the structure.** It lists the nodes and the dependency edges
between them; each entry resolves to `nodes/<id>/` for that node's *behavior*
(and its assets — `prompt.md`, `skills/`). This keeps the DAG shape in one place
and each node package focused on what the node *does*:

```yaml
name: my-graph
nodes:
  - id: recon                    # a node with no deps
  - id: sink-scan
    deps: [recon]                # dependency edges live here, in graph.yaml
  - id: analyze
    deps: [sink-scan]
```

In this **explicit-topology** mode `node.yaml` must *not* declare `deps` (that
would be a second source of truth — `init` rejects it). A node id may also be
written as a bare string (`- recon`) when it has no deps.

**Legacy auto-discovery.** If `graph.yaml` carries only `name` (no `nodes:`
list), the loader instead scans `nodes/` for every folder with a `node.yaml` and
takes each node's `deps` from its own `node.yaml`. Kept for backward
compatibility; prefer the explicit form above.

`graphgine init <dir>` loads the package; `graphgine init <file>.yaml` still loads
the inline form. Skills for an `agent` node are the union of those auto-discovered
under its `skills/` folder and those declared in `node.yaml` `skills:` — by path
(relative to the node package) or by name.

### Skill binding

A node's skills belong to that node's **sub-agent**, not to the driver's prompt.
The CLI never reads a skill body; it resolves each entry and tells the driver
*how to attach it*. The bind name is the skill's **directory name** — that is
what the host registers a skill under, whatever its frontmatter says.

```jsonc
"skills": [
  {"name": "memory-corruption-triage", "source": "user",
   "path": "/home/me/.claude/skills/memory-corruption-triage/SKILL.md"},
  {"name": "sink-walk", "source": "node",   // ships in the node package,
   "path": "/abs/.../nodes/analyze/skills/sink-walk/SKILL.md"}   // loaded into the project root by init
]
```

The driver attaches each skill **by name**: it tells the sub-agent to
`Use the /<name> skill`, and the Skill tool loads the body *and* the skill's own
tool permissions into the sub-agent's context. `init` has loaded every skill into
the project skill root (below), so the name resolves. `path` is where the skill
lives on disk — informational, and a **fallback** the sub-agent reads directly if
the name is not yet resolvable (a link newer than the session, or a `conflict` /
`failed` load). Either way the skill text never enters the driver's context and is
never pasted into a prompt.

**`init` registers, and records.** A skill that ships inside a graph lives where
the host never looks, so `graphgine init` reads each agent node's skill
directory, links what it finds into the **project** skill root (`<cwd>/.claude/skills`)
under its directory name, and **records the resulting bindings in the run**:

```
<project>/.claude/skills/sink-walk -> /abs/.../nodes/analyze/skills/sink-walk
```

The project root — not the user's global `~/.claude` — is the default on purpose:
a graph's bundled skills stay **scoped to the project the run happens in**, don't
outlive the run, and don't collide with other projects' skills of the same name.
The skill stays in the graph — only a link is created — and it gains a name to
bind. Every later spawn attaches the skills from that record, so a resumed run
uses exactly what `init` decided. `init` reports each registration in `skills[]`
(`registered` | `linked` | `conflict` | `failed`); existing names are never
overwritten, and a node whose skill could not be loaded reaches its sub-agent via
the path. `--skills-dir` (or `$GRAPHGINE_SKILLS_LINK_DIR`) chooses a different root.

Because the host scans its skill roots **when a session starts**, a skill linked
by this `init` may not resolve by name until the next session; sub-agents read it
by path this run (`init`'s `message` says so). `finalize` then removes the links
(see below), so they never outlive the run.

**`finalize` cleans up.** The links are per-run: `graphgine finalize` removes the
ones this run created (and only those — a link a different skill has since taken
over, or one already gone, is left alone) and lists them in `skills_unlinked`.
The skills themselves stay in the graph; only the project-root links are undone,
so a run leaves the project's `.claude/skills` as it found it.

`agent: <subagent-type>` on an `agent` node names the sub-agent type to spawn
(it arrives as `spec.agent`). Point it at an agent definition whose frontmatter
preloads the node's skills when you want the binding to happen at spawn time:

```yaml
---
name: my-node-worker
tools: Read, Write, Grep, Glob, Bash, Skill
skills: [memory-corruption-triage]
---
```

### Scatter / gather

`foreach: ${node.output.field}` fans a node out into one instance per array
element at runtime (the declared graph stays immutable — expansion is a
deterministic function of the data). Instances run in parallel under
`--concurrency`, each with its own workspace / status / retry. Downstream nodes
that depend on the scatter node **gather**: they wait for all instances
(barrier), non-fatal failed instances are dropped, and the downstream receives
the **array of survivor outputs**. A *fatal* instance failure aborts the task.
v1 is flat only (no nested scatter) and always barriers (no streaming gather).

**Two fan-in modes.** Graph-level scatter's fan-in is the *gather* above. When a
single `agent` node instead fans out *internally* over a runtime-discovered list
(no `foreach`), its fan-in is the sub-agent **aggregating its results into the
node's one output** (matching `output_schema`, e.g. `{"results":[...]}`). The
engine sees one node with one output; the aggregation lives inside the sub-agent.

### Failure semantics

`success | failed | skipped`. A `failed` node with retries left is requeued.
Exhausted retries → `failed`; if the node is `fatal` the whole task fails,
otherwise its dependents are **cascade-skipped** (a dependent listed in
`optional_inputs` still runs, with that input marked `ABSENT`), and independent
branches continue.

## CLI verbs (the main-agent ↔ CLI protocol)

All commands print JSON.

| command | behavior |
|---------|----------|
| `init <graph.yaml> [--concurrency 10] [--skills-dir DIR]` | validate DAG (acyclic, deps exist, no nested scatter), register each agent node's skills with the host and record the bindings, create the run + workspaces, print `run_id` + initial frontier |
| `next <run_id>` | expand ready scatters, **run all ready command nodes directly**, gather, cascade-skip, and return the ready `agent`/`human` work (`status: work\|waiting\|done\|failed`) |
| `report <run_id> <node_id> --status success\|failed [--output FILE]` | validate the node's output against its schema; on success advance the DAG; on schema failure reject (retry per policy) |
| `status <run_id>` | render the DAG state |
| `finalize <run_id>` | summarize; exit code 1 iff a fatal node failed |

## Driving the graph (the main agent's loop)

The entry point for Claude Code is the **`graphgine` driver skill** at
[`.claude/skills/graphgine/SKILL.md`](.claude/skills/graphgine/SKILL.md) — it
teaches Claude Code to be the driving main agent (init → next → execute
agent/human work → report → finalize). It is auto-discovered when you run Claude
Code in this repo; to use it while hunting in other projects, copy it to
`~/.claude/skills/graphgine/`.

The main agent keeps only global context; each node's real work runs in an
isolated sub-agent so node contexts never pile up.

```
init graph.yaml                      → run_id
loop:
    r = next run_id
    if r.status == "work":
        for each spec in r.todo:
            if spec.type == "agent":
                spawn an isolated sub-agent (spec.agent, else sec-graph), with:
                    - spec.skills bound to it (by name; by path when unregistered)
                    - the tools in spec.tools
                    - spec.input and the files under spec.input_dir
                    - spec.prompt
                have it write its result JSON to spec.output_dir/output.json
            if spec.type == "human":
                show spec.prompt (+ input_dir) to a person; collect their JSON decision
            report run_id spec.node_id --status success|failed
    elif r.status in ("done", "failed"):
        break
finalize run_id
```

Because `graphgine` runs command nodes itself and only hands back `agent`/`human`
work, the loop above is all the driver needs — and the CLI stays agent-agnostic
(it never touches an LLM; it resolves skill *bindings* and never reads a skill).

## Workspace layout & resume

```
runs/<run_id>/
  state.json                 # the whole state machine (resume reads this)
  nodes/<node_id>/input/     # each dependency's declared output, materialized here
  nodes/<node_id>/output/output.json
  nodes/<node_id>/logs/      # stdout/stderr for command nodes
```

Every node has an isolated workspace; a downstream node sees only the outputs of
its declared deps (materialized into `input/<dep>/`), so its input set is fully
reproducible. State + workspaces persist across invocations, so a crashed or
paused run resumes from wherever `next` left off — already-successful nodes keep
their cached outputs.

## Not in v1

Conditional edges / data-driven branch selection; nested scatter / streaming
gather; a node-type plugin system; non-Claude-Code agent backends (only the
binding contract is defined); a skill/graph registry.
