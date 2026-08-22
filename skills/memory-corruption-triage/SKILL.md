---
name: memory-corruption-triage
description: Triage a single candidate sink in a binary for memory-corruption bugs (overflow, UAF, format string) and produce a structured verdict.
# --- framework superset fields (agent-agnostic; injected by graphgine, not the host) ---
inputs:
  - name: candidate
    description: One candidate sink (function/xref) discovered upstream.
allowed-tools: [read, bash]
---

# Memory-corruption triage (single candidate)

You are triaging **one** candidate sink for a memory-corruption vulnerability.
Work only on the candidate handed to you via the node input; do not wander the
whole binary.

## Method
1. Read the candidate's context from the input directory (upstream `sink-scan`
   output is materialized under `input/`).
2. Reconstruct how attacker-influenced data reaches the sink (source → sink).
   Note the calling convention, buffer sizes, and bounds checks.
3. Classify: stack/heap overflow, use-after-free, double-free, format string,
   integer overflow leading to undersized allocation, or "no bug".
4. Judge reachability and attacker control. Prefer "needs-verification" over a
   confident claim you cannot back with evidence.

## Output (must satisfy the node's output_schema)
Return JSON:
```json
{
  "verdict": "vulnerable | needs-verification | not-vulnerable",
  "class": "stack-overflow | heap-overflow | uaf | format-string | int-overflow | none",
  "evidence": "short justification citing the code path",
  "confidence": "low | medium | high"
}
```
