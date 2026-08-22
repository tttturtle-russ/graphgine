---
name: graphgine
description: Drive a graphgine graph — run a declared YAML DAG of vuln-hunting (or other) subtasks to completion. Use when the user wants to run/execute/drive a graphgine graph or a graph.yaml, orchestrate a declared multi-agent workflow, or "run this hunting graph". graphgine is the zero-LLM Go state machine; THIS skill is how you (the coding agent) act as its driving main agent: init → next → execute agent/human work → report → finalize.
---

# Driving a graphgine graph

`graphgine` is a zero-LLM Go orchestration state machine. It parses/validates a
declared YAML DAG, **runs `command` nodes itself**, validates every node's
output against a schema, and persists everything (resumable). It hands the
intelligent work back to **you, the main agent**. Your job is the whole run
below — starting from `init`.

You keep only *global* context (the run id, overall goal, progress). Each
node's real work runs in an **isolated sub-agent** so node contexts never pile
up in yours.

## You own the whole run

The user gives you a **single instruction** naming a graph — a `graph.yaml` file
or a node-package directory (e.g. "run the hunt graph in `~/vuln-testbed/hunt`").
From there **you do everything, autonomously**: build the binary if needed,
`init` the run, drive the loop (spawn sub-agents for agent nodes, handle human
gates), `finalize`, and report the results back to the user. **Never ask the
user to run a `graphgine` command themselves** — every `init`/`next`/`report`/
`finalize`/`status` call is yours to make. If the graph path is ambiguous, look
for a `graph.yaml` or a `nodes/` directory at/near where the user pointed.

**Who runs what — keep the two levels separate:**

- **You (the main agent) run every `graphgine` CLI call yourself, at the top
  level** — a plain `Bash` call (e.g. `Bash(graphgine next <run_id>)`). Never
  delegate a `graphgine` call into a sub-agent; the CLI is the orchestrator and
  it belongs to you. (These `Bash(graphgine …)` calls may render collapsed in the
  UI — that's just the terminal folding tool calls, not a nesting issue; expand
  with `Ctrl+O`.)
- **Only per-node work is delegated** — you spawn one isolated sub-agent per
  `agent` node that `next` hands back (see below), and it never touches
  `graphgine`.

## 0. Prerequisites

- The `graphgine` binary (`$GG` below; e.g. `graphgine` if installed, or
  `./bin/graphgine`). If missing, build it: `go build -o bin/graphgine ./cmd/graphgine`.
- A graph — either a single YAML file, or a **node-package directory** (below).
- The CLI resolves each `agent` node's skills for you and puts them in the spec
  as *bindings* (`spec.skills[]`), not as text — see "Binding the skills" below.
  Neither the CLI nor you ever read a skill body.
- Every CLI call prints JSON. Parse it; don't eyeball it.

### Graph & node layout

A graph can be one `graph.yaml` file (nodes inline), or a **package directory**:

```
my-graph/
  graph.yaml                     # { name: ... }
  nodes/
    <node-id>/                   # dir name is the node id
      node.yaml                  # type, deps, output_schema, foreach, ...
      prompt.md                  # optional; the sub-agent / human prompt
      skills/<skill>/SKILL.md    # optional; bound to this node's sub-agent
```

A node declares its skills by **path** (they travel with the graph) or by name:

```yaml
skills:
  - ./skills/memory-corruption-triage   # ships with the node package
  - memory-corruption-triage            # already registered with the host
```

The skill's **directory name** is the name it binds under — that is what the
host registers it as, whatever the SKILL.md frontmatter says.

`init` accepts either form (a file or the directory).

## 1. init  (your first action)

```sh
$GG init <graph.yaml | graph-dir> --concurrency 10
```
Read `run_id` from the JSON and keep it for the whole run. (Pass `--runs DIR` or
set `$GRAPHGINE_RUNS` consistently across every call.) If the graph declares env
the command nodes need (e.g. `$TESTBED`), export it before you start.

`init` also does the run's **skill pass**: it reads each agent node's skill
directory, registers what it finds with the host — a link into the **project**
skill root (`<cwd>/.claude/skills`), under the skill's directory name, so the
skill gains a name to bind while staying in the graph — and **records the
bindings in the run**. The link is per-run and scoped to the project (not the
user's global `~/.claude`); `finalize` removes it. Every `next` hands those
recorded bindings back; you never re-resolve them. Check `skills[]` and
`message` in the JSON:

- `"status": "registered"` — already in place; the name resolves.
- `"status": "linked"` — loaded into the project root just now. The host scans
  skill roots when a session **starts**, so if a sub-agent can't find the name
  yet it reads the skill's `.path` this run (`finalize` later removes the link).
  Worth telling the user once, at the end.
- `"status": "conflict"` / `"failed"` — the name is taken, or the link failed
  (`detail` says why); those skills reach the sub-agent only via `.path`.

`--skills-dir` registers into a different root.

## 2. the loop: next → do work → report

```sh
$GG next <run_id>
```
Branch on `status`:

- **`work`** — `todo` is a list of node specs you must execute now. They are
  independent; **run them in parallel** (spawn multiple sub-agents in one
  message). Each spec has: `node_id`, `type` (`agent`|`human`), `agent`,
  `prompt`, `skills`, `tools`, `input`, `input_dir`, `output_dir`,
  `output_schema`, `attempt`. After executing each, `report` it, then call `next` again.
- **`done`** — go to Finalize.
- **`failed`** — a fatal node failed; the task is aborted. Go to Finalize (it
  will report the failure), then tell the user what failed (`status` shows which
  node + message).
- **`waiting`** — you have dispatched work still unreported. Report it, then
  `next` again.

### Executing an `agent` node

Spawn an isolated sub-agent with the **Task tool**, using `spec.agent` as the
`subagent_type` if the node declares one, else **`sec-graph`** (the graphgine
node-worker agent, installed to `~/.claude/agents/sec-graph.md`). Using
`sec-graph` — rather than the default `general-purpose` — is what makes each
node worker show as **"sec-graph"** in the UI and gives it the node-worker
operating rules. If that agent type isn't registered (skill used outside an
install), fall back to a general sub-agent. Compose its context from the spec:

1. **Bind the skills — never paste them.** See below.
2. **Constrain tools** to `spec.tools` when your sub-agent mechanism supports it.
3. **Provide inputs**: `spec.input` (already resolved — for a scatter instance
   it holds this instance's `${item}`), plus the files under `spec.input_dir/`
   (each dependency's output is materialized in `input/<dep>/`).
4. **Give the task**: `spec.prompt`.
5. **Demand structured output**: the sub-agent must write its result as JSON to
   `spec.output_dir/output.json`, satisfying `spec.output_schema`.

### Binding the skills

A node's skills belong to the **sub-agent**, not to your prompt. Do **not** read
`SKILL.md` files and do **not** paste skill text into the sub-agent's
instructions — that burns your context on knowledge you will never use, and
loses the skill's own tool permissions. Bind by name instead: for each
`spec.skills[]` entry, put a line like `Use the /<name> skill.` in the
sub-agent's prompt. The sub-agent invokes it with the **Skill tool** and
receives the body *and* the skill's tool permissions in **its own** context.
`init` has loaded every skill into the project skill root, so the name resolves.

`.path` is where the skill lives on disk — informational, and a **fallback**:
if a sub-agent reports `Unknown skill: <name>` (the link is newer than the
session's skill scan), tell it to **read `.path` first** and follow it. Still no
pasting — the sub-agent does the reading. `init`'s `skills[]` flags any skill it
could not load (`conflict` / `failed`); for those, the `.path` fallback is the
only route.

For a node that always runs under the same skills, the strongest binding is a
dedicated agent definition — a `.claude/agents/<name>.md` whose frontmatter
preloads them:

```yaml
---
name: my-node-worker
description: ...
tools: Read, Write, Grep, Glob, Bash, Skill
skills: [memory-corruption-triage]   # preloaded at spawn, before the first turn
---
```

and then `agent: my-node-worker` in that node's `node.yaml`, which arrives as
`spec.agent`. Use it when a node's skill set is fixed; use the two rules above
for everything else.

### Executing a `human` node

Present `spec.prompt` (and relevant files under `spec.input_dir/`, e.g. the
gathered findings) to the user — use AskUserQuestion for a simple approve/reject,
or write a short review document for richer input. Collect their decision as
JSON matching `spec.output_schema` (e.g. `{"decision":"approve"}`) and write it
to `spec.output_dir/output.json`.

### report

```sh
$GG report <run_id> <node_id> --status success        # output.json already written
$GG report <run_id> <node_id> --status failed         # the node could not be completed
```
If the result reports `"accepted": false`, the output failed schema validation.
Read `schema_errors`, fix the sub-agent's output (or re-run it), and report
again — graphgine will have requeued the node if it has `retry` budget.

## 3. finalize

```sh
$GG finalize <run_id>
```
Exit code is non-zero iff a fatal node failed. Summarize the run for the user
from the `summary` (and `$GG status <run_id>` for per-node detail). `finalize`
also removes the per-run skill links `init` created in the project skill root;
the names it undid are listed in `skills_unlinked`. **Always finalize**, even on
failure, so those project-scoped links don't leak.

## Parallelism

`next` returns every currently-runnable agent/human node at once (bounded by
`--concurrency`, and scatter instances arrive as separate `node_id`s like
`analyze[3]`). Spawn them concurrently — one message, multiple sub-agent calls —
then report each as it finishes and call `next` again for the next wave.

## Fan-out and fan-in

- **Graph-level scatter** (`foreach:` in the node): graphgine expands it into
  instances and **gathers** for you — the downstream node automatically receives
  the array of survivor outputs as its input. You just execute each instance.
- **Node-internal dynamic fan-out**: when a single `agent` node must process a
  runtime-discovered list *without* declaring a graph scatter, instruct its
  sub-agent to iterate internally and **aggregate (fan-in) its results into the
  node's single output**, matching `output_schema` (e.g.
  `{"results":[...],"summary":...}`). The engine sees one node with one output;
  the aggregation happens inside the sub-agent.

## Resume & observe

State and workspaces persist under `runs/<run_id>/`. To resume a paused or
crashed run, just call `next <run_id>` again — succeeded nodes keep their cached
outputs. `status <run_id>` renders the current DAG state at any time.

## Minimal loop (reference)

```
run_id = init graph.yaml
loop:
    r = next run_id
    if r.status in (done, failed): break
    for spec in r.todo (in parallel):
        if spec.type == agent: sub-agent(bind skills; +tools+input+prompt) -> output_dir/output.json
        if spec.type == human: ask the user -> output_dir/output.json
        report run_id spec.node_id --status success|failed
finalize run_id
```
