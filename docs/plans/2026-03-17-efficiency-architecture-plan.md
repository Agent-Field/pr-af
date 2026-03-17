# PR-AF Efficiency & Architecture Improvement Plan

**Date**: 2026-03-17
**Goal**: 2-3x speedup on large repos without quality loss, plus archei-compliant data flows.

---

## Change 1: Streaming Meta-Selector → Review Pipeline

### Problem

Currently `_run_meta_selectors` uses `asyncio.gather` and blocks until ALL 3 lenses (semantic, mechanical, systemic) complete before ANY review dimensions start. On large repos, each lens takes 5-15 minutes. The pipeline sits idle for ~10 min waiting for the slowest lens.

### Current Flow (blocking)

```
semantic  ████████████████░░░░░░░  (done at 6 min)
mechanical ███████████████████████  (done at 12 min)
systemic  █████████████████░░░░░░  (done at 8 min)
                                    │
                             wait for ALL ← 12 min idle
                                    │
                                    ▼
                          review dimensions start at 12 min
```

### Proposed Flow (streaming)

```
semantic  ████████████████░░░░░░░  (done at 6 min)
                    │
                    ├──→ semantic dims start reviewing at 6 min
                    │
mechanical ███████████████████████  (done at 12 min)
                              │
                              ├──→ mechanical dims start at 12 min
                              │
systemic  █████████████████░░░░░░  (done at 8 min)
                        │
                        ├──→ systemic dims start at 8 min
                        │
                        ▼
              cross-meta dedup runs incrementally as each lens completes
              review layer collects findings from ALL dims via queue
```

### Implementation

Change `_run_meta_selectors` to stream dimensions via an `asyncio.Queue` instead of collecting all at once:

```python
async def _run_meta_selectors_streaming(
    self, intake, anatomy, review_depth, dimensions_queue
):
    """Stream dimensions to queue as each lens completes."""

    async def run_lens_and_emit(lens_name):
        result = await run_lens(lens_name)
        # Prefix dimension IDs with lens name
        dims = [dim.model_copy(update={"id": f"{result.lens}_{dim.id}"})
                for dim in result.dimensions]
        # Emit to queue immediately — reviewers can start
        await dimensions_queue.put(dims)
        return result

    # All 3 lenses run in parallel, each emits to queue when done
    meta_results = await asyncio.gather(
        run_lens_and_emit("semantic"),
        run_lens_and_emit("mechanical"),
        run_lens_and_emit("systemic"),
    )
    await dimensions_queue.put(None)  # Sentinel
    return meta_results
```

The `run()` pipeline changes from:

```python
plan = await self._run_meta_selectors(...)      # BLOCKS until all done
await self._run_parallel_review(plan, ...)        # Then starts
```

To:

```python
dims_queue = asyncio.Queue()
meta_task = asyncio.create_task(
    self._run_meta_selectors_streaming(intake, anatomy, depth, dims_queue)
)
review_task = asyncio.create_task(
    self._run_streaming_review(dims_queue, findings_queue)
)
# Both run concurrently — reviews start as dims arrive
```

The `_run_streaming_review` reads from `dims_queue`, and for each batch of dimensions, launches parallel reviewers. Cross-meta dedup happens incrementally: as a new lens's dimensions arrive, dedup against already-running dimensions before launching.

### Expected Speedup

Current: 12 min (wait for slowest lens) + 8 min (reviews) = **20 min**
Proposed: Reviews start at 6 min (first lens done). By 12 min, semantic and systemic reviews are mostly done. Total ≈ **14 min** (~30% faster).

### Risk

Cross-meta dedup becomes incremental instead of batch. We may launch a dimension that later turns out to be a duplicate of one from a slower lens. Mitigation: the dedup is by target_files overlap — we can still check new dimensions against already-launched ones and skip if overlap > 80%.

---

## Change 2: Scout/Strategist Split for Meta-Selectors

### Problem

Each meta-selector `.harness()` browses the entire repo sequentially. For a 5-cluster PR, the harness reads cluster A's files, then B's, then C's — all serially within one agent session.

### What We're Splitting

**Before**: One `.harness()` per lens that does both investigation (browsing files) AND reasoning (crafting dimensions).

**After**: N parallel `.harness()` "scouts" (one per cluster) that do investigation, feeding into one `.harness()` "strategist" that does reasoning.

### Scout (`.harness()`)

Why `.harness()`: Needs tool access to browse repo files, trace callers, read imports. Multi-turn — reads X, decides to read Y.

**Input**:
- `cluster_id: str` — which cluster to investigate
- `cluster_files: list[str]` — files in this cluster
- `lens_focus: str` — which lens (semantic/mechanical/systemic) as a string prompt section
- `pr_context: str` — PR narrative, risk surfaces (string, per archei rules — LLM reads this)
- `cross_cluster_edges: str` — programmatically computed dependency edges involving this cluster (string for LLM)
- `repo_path: str` — for tool access

**Output schema** (flat, 3 fields — follows archei rules):

```python
class ClusterScoutReport(BaseModel):
    cluster_id: str       # Structured — code uses for grouping
    investigation: str    # STRING — full narrative report for strategist LLM
    confident: bool       # Structured — code uses for fallback decision
```

The `investigation` string contains everything the scout found: functions changed, callers traced, risk signals, suggested dimension ideas. It's natural language because only the strategist LLM consumes it — no code parses it.

We do NOT put `key_functions_changed`, `callers_discovered`, etc. as structured fields because no code routes on them. That would violate: "You're passing structured JSON to another LLM that just reads it as text → use string instead."

### Strategist (`.harness()`, but no tool use needed)

Why `.harness()` not `.ai()`:
- Output is `MetaDimensionResult` with nested `ReviewDimension` objects containing narrative `review_prompt` fields — violates `.ai()`'s "flat, 2-4 attributes" rule
- Input is all scout reports (~2500+ tokens for deep reviews) — exceeds `.ai()`'s comfort zone

Why not `.ai()`: The decision tree says "Produce output > 4 fields or narrative text? → .harness()". `MetaDimensionResult` has nested lists of complex objects. Firmly `.harness()`.

**Input** (per archei rules — context for LLM → string):
```
## Scout Reports

### Cluster: src/middlewared/plugins/zfs (confident: true)
[Scout's full narrative investigation report...]

### Cluster: src/middlewared/plugins/pool_ (confident: true)
[Scout's full narrative...]

## Cross-Cluster Dependency Edges (computed programmatically)
- load_key() in zfs/encryption.py ← called from pool_/encryption_lock.py
- check_key() in zfs/encryption.py ← called from pool_/encryption_info.py

## PR Context
Type: refactor, Complexity: standard
Risk signals: [...]
```

All passed as a **single string prompt**. No JSON dumps for the LLM to parse.

**Output**: Same `MetaDimensionResult` schema as today (hybrid — structured for code routing, strings for downstream LLM reviewers).

### Optional: Pre-Scout Triage Gate (`.ai()`)

Per cluster, a fast `.ai()` gate to skip irrelevant clusters:

```python
class ClusterTriageGate(BaseModel):
    worth_scouting: bool   # Code routes on this
    confident: bool        # Fallback trigger
```

Input: ~200 tokens (cluster name, file list, PR type). Textbook `.ai()` — fast classification, flat schema. Runs in parallel for all clusters, costs ~$0.001 total.

### Data Flow Diagram

```
Anatomy (5 clusters) + dependency_graph
    │
    ├─ [Code] compute cross-cluster edges from dependency_graph
    │
    ├─ (optional) .ai() triage per cluster → skip irrelevant ones
    │
    ├─ Scout(cluster_0, lens="semantic") ─┐
    ├─ Scout(cluster_1, lens="semantic") ─┤  parallel .harness()
    ├─ Scout(cluster_2, lens="semantic") ─┤  each browses only its files
    ├─ Scout(cluster_3, lens="semantic") ─┤
    └─ Scout(cluster_4, lens="semantic") ─┘
                                           │
                                           ▼
                                 [Code] concatenate scout
                                 reports as narrative string
                                           │
                                           ▼
                         Strategist(.harness(), no tool use)
                         Input: all scout narratives (string)
                               + cross-cluster edges (string)
                               + PR context (string)
                         Output: MetaDimensionResult (hybrid)
```

### Expected Speedup

Current per lens: ~10-15 min (one harness browsing entire repo serially)
Proposed per lens: ~4 min (5 scouts parallel, each browses 1-2 files) + ~2 min (strategist reasoning) = **~6 min**

Combined with Change 1 (streaming), the first lens's dimensions would reach reviewers in ~6 min instead of ~12 min.

---

## Change 3: Archei-Compliant Data Flows (Schema Audit Fixes)

### Problem

Several inter-agent data flows pass `model_dump()` JSON to downstream LLMs, violating the archei rule: "Context for another LLM agent → String."

### Fix 3a: Evidence Verifier Input

**Current** (violation):
```python
verifier_raw = await evidence_verifier(
    findings=[f.model_dump() for f in findings],  # JSON dicts to LLM
    ...
)
```

**Proposed**: Pass findings as natural language with reference keys:
```python
findings_narrative = _format_findings_for_llm(findings, evidence_map)
# Returns something like:
# "[F1] 'Missing error handler in load_key()' (critical, confidence 0.8)
#   File: zfs/encryption.py:45
#   Claim: load_key() doesn't handle ZFSException...
#   Evidence: [code snippet]
#
#  [F2] 'Unused parameter tls' (suggestion, confidence 0.6)
#   ..."
```

The verifier returns results keyed by reference ID (`[F1]`, `[F2]`) instead of exact title matching. Code maps reference IDs back to findings.

Why this is better: The LLM reads natural language, not JSON. It can reason about the finding's claim vs the evidence more naturally. The reference keys ensure programmatic matching downstream.

### Fix 3b: Adversary Phase Input

Same pattern as 3a. Currently `json.dumps(findings_with_evidence)` — replace with `_format_findings_for_llm()`.

### Fix 3c: Compound Finder Input

Same pattern. Currently passes `json.dumps(payload)` of cluster findings and evidence.

### Fix 3d: OutputCalibrationGate `.ai()` Fallback

**Current**: Always `.ai()` regardless of input size.

**Proposed**: Apply the `.ai()` fallback pattern from CLAUDE.md:
```python
if len(scored) <= 5:
    # Small input — .ai() is fine
    calibration = await output_calibration_gate(scored)
else:
    # Large input — use .harness() for deeper reasoning
    calibration = await output_calibration_harness(scored)
```

The `.ai()` gate stays for small PRs (fast, cheap). For large PRs, a `.harness()` does the calibration with access to read actual code if needed.

### Fix 3e: Scout Report Schema (from Change 2)

Already designed correctly: 3 flat fields, `investigation` as string for LLM consumption.

---

## Change 4: Streaming Meta-Selector + Review Integration Detail

### The Full Streaming Pipeline

This combines Changes 1 and 2. The complete flow becomes:

```
Phase 1: INTAKE (.ai() + fallback)
    │
Phase 2: ANATOMY (code + .harness())
    │
Phase 3: META-SELECTORS (streaming, with scouts)
    │
    ├─ For each lens (semantic, mechanical, systemic) — parallel:
    │   ├─ [Code] compute cross-cluster edges
    │   ├─ Scout per cluster — parallel .harness()
    │   ├─ Strategist — .harness() → MetaDimensionResult
    │   └─ Emit dimensions to dims_queue immediately
    │
    ├─ Dims Consumer (runs concurrently with meta-selectors):
    │   ├─ Reads dims from queue as they arrive
    │   ├─ Incremental cross-meta dedup against already-launched dims
    │   └─ Launches review_dimension .harness() per dim immediately
    │
Phase 4+5: REVIEW + LAYER (streaming, starts as dims arrive)
    │   ├─ Each review_dimension emits findings to findings_queue
    │   ├─ Evidence extraction starts on early findings
    │   ├─ Relevance gate runs on early findings
    │   └─ Adversary batches start as enough findings accumulate
    │
Phase 6: SYNTHESIS (after all reviews + adversary complete)
    │
Phase 7: OUTPUT
```

Key insight: Phases 3 and 4 now overlap. We don't wait for all dimensions before starting reviews. The pipeline is a streaming DAG, not a sequential waterfall.

### Concurrency Safety

- `dims_queue`: Multiple lens tasks produce, one consumer reads → standard producer-consumer with sentinel
- `findings_queue`: Multiple review tasks produce, layer task reads → already works this way today
- Cross-meta dedup: Protected by a lock or done in the single consumer coroutine
- Budget tracking: Already uses `self.total_cost_usd` which is single-threaded (asyncio, no threads)

---

## Priority Order

| # | Change | Speedup | Effort | Risk |
|---|--------|---------|--------|------|
| 1 | Streaming meta→review pipeline | ~30% | Medium | Low — queue pattern already used for findings |
| 2 | Scout/strategist split | ~50% on large repos | High | Medium — new harness functions, need testing |
| 3 | Archei data flow fixes | Quality improvement | Medium | Low — prompt changes, same logic |
| 4 | Combined streaming + scouts | ~60% total | Included in 1+2 | Included |

Recommended order: **3 → 1 → 2**. Fix data flows first (correctness), then add streaming (easy win), then scouts (biggest change).

---

## What This Plan Does NOT Change

- Review dimension quality — same prompts, same investigation depth
- Adversary/evidence verification logic — same challenge protocol
- Scoring/synthesis — same deterministic scoring
- Output format — same GitHub comments/review format
- Budget caps — same cost controls

The changes are purely about parallelism, data flow compliance, and removing unnecessary blocking. Same quality, faster execution.
