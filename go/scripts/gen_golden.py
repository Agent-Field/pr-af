#!/usr/bin/env python3
"""Committed golden-fixture generator for the Go prompt-builder port (T2.4).

This script is the SINGLE SOURCE OF TRUTH for the byte-verbatim prompt fixtures
under ``go/internal/prompts/testdata/``. It imports the REAL Python prompt
builders from ``pr_af`` and captures the exact prompt/system strings they emit
for a fixed set of A/B/C fixture inputs, then writes them to
``<builder>_<case>.txt``. The Go golden tests embed those files and assert the
Go builders reproduce them byte-for-byte.

HOW IT WORKS
------------
Every PR-AF reasoner builds its LLM prompt inline and hands it to
``router.app.ai(...)`` / ``router.app.harness(...)``. We bind ``router._agent``
to a capturing fake, drive each reasoner with explicit fixture inputs, and
record every (method, system, prompt) tuple. Two builders expose importable
units we call directly instead: ``merge_gate._build_user_prompt`` /
``merge_gate._MERGE_GATE_SYSTEM`` and ``polish._POLISH_SYSTEM``. One builder
lives in ``orchestrator._build_gap_dimensions`` (a method); its one-line
f-string is reconstructed here verbatim.

Fixture cases:
  A = all optional branches populated (rich data, inline context)
  B = minimal / else-branches (empty optionals)
  C = the large-context "written to file" branch (repo_path set, payload > limit)

REPRODUCE (from the pr-af repo root):
  /tmp/claude-1000/-home-abir-gb/e0447ca2-28f4-49fe-ae8a-ead45bdad68c/scratchpad/praf-venv/bin/python go/scripts/gen_golden.py

Or with any interpreter that has pr-af installed / on PYTHONPATH=src:
  PYTHONPATH=src python go/scripts/gen_golden.py

The script is deterministic and idempotent: rerunning overwrites the fixtures
with identical bytes unless a Python builder changed (which is exactly the
signal the Go golden tests exist to catch).
"""

from __future__ import annotations

import asyncio
import os
import sys

# Make `pr_af` importable when run from the repo root without install.
_REPO_ROOT = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
_SRC = os.path.join(_REPO_ROOT, "src")
if os.path.isdir(_SRC) and _SRC not in sys.path:
    sys.path.insert(0, _SRC)

from pr_af import merge_gate, polish  # noqa: E402
from pr_af.reasoners import harnesses, router  # noqa: E402
from pr_af.schemas.output import ScoredFinding  # noqa: E402

TESTDATA = os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), "internal", "prompts", "testdata")
# Scratch dir used only for the file-write ("written to: <path>") branches. The
# reasoners write a context file under <repo>/.pr-af-context/; we point it at a
# STABLE, fixture-controlled path so the emitted message is deterministic.
FIXTURE_REPO = "/tmp/pr-af-fixture-repo"


# ---------------------------------------------------------------------------
# Capturing fake router.app
# ---------------------------------------------------------------------------
_CAPTURED: list[tuple[str, str | None, str]] = []


class _FakeResult:
    def __init__(self) -> None:
        self.parsed = None
        self.error_message = None


class _FakeGate:
    # Attributes read by intake_phase / coverage_gate after the call. Forcing
    # confident=False routes intake_phase through BOTH the .ai gate prompt and
    # the .harness fallback prompt so we capture both.
    pr_type = "feature"
    complexity = "standard"
    confident = False
    fully_covered = False
    gap_descriptions: list[str] = []

    def model_dump(self) -> dict:
        return {}


class _FakeApp:
    async def ai(self, prompt, system=None, schema=None, response_format=None):
        _CAPTURED.append(("ai", system, prompt))
        return _FakeGate()

    async def harness(self, prompt, schema=None, cwd=None):
        _CAPTURED.append(("harness", None, prompt))
        return _FakeResult()


router._agent = _FakeApp()


def cap(coro) -> list[tuple[str, str | None, str]]:
    """Run one reasoner coroutine and return the prompts it captured."""
    _CAPTURED.clear()
    asyncio.get_event_loop().run_until_complete(coro)
    return list(_CAPTURED)


def emit(name: str, text: str) -> None:
    path = os.path.join(TESTDATA, name + ".txt")
    with open(path, "w", encoding="utf-8") as f:
        f.write(text)
    print(f"  wrote {name}.txt ({len(text)} bytes)")


# ---------------------------------------------------------------------------
# Fixture input factories
# ---------------------------------------------------------------------------
def changed_file(path, status="modified", additions=0, deletions=0):
    return {"path": path, "status": status, "additions": additions, "deletions": deletions}


def pr_data(**kw):
    base = {
        "owner": "acme",
        "repo": "widget",
        "number": 7,
        "title": "Add retry logic",
        "description": "",
        "labels": [],
        "author": "",
        "commit_messages": [],
        "diff": "",
        "changed_files": [],
    }
    base.update(kw)
    return base


def intake_dict(**kw):
    base = {
        "pr_type": "feature",
        "complexity": "standard",
        "languages": ["python"],
        "areas_touched": ["api"],
        "risk_signals": ["changes API surface or request/response behavior"],
        "ai_generated": 0.0,
        "review_depth": "standard",
        "pr_summary": "Adds a retry wrapper around the HTTP client.",
    }
    base.update(kw)
    return base


def anatomy_dict(**kw):
    base = {
        "files": [],
        "clusters": [],
        "blast_radius": [],
        "dependency_graph": {},
        "stats": {
            "total_files": 0,
            "total_additions": 0,
            "total_deletions": 0,
            "files_added": 0,
            "files_modified": 0,
            "files_removed": 0,
            "files_renamed": 0,
            "test_files_changed": 0,
            "test_to_code_ratio": 0.0,
        },
        "pr_narrative": "",
        "risk_surfaces": [],
        "unrelated_changes": [],
        "intent_gaps": [],
        "context_notes": "",
    }
    base.update(kw)
    return base


def cluster(cid, name, files, primary_language="python", description="desc"):
    return {
        "id": cid,
        "name": name,
        "files": files,
        "primary_language": primary_language,
        "description": description,
    }


def finding(**kw):
    base = {
        "dimension_id": "d1",
        "dimension_name": "Retry semantics",
        "file_path": "client.py",
        "line_start": 10,
        "line_end": 12,
        "hunk_context": "",
        "severity": "important",
        "title": "Retry loop can spin forever",
        "body": "The loop never decrements the counter.",
        "suggestion": None,
        "evidence": "Step 1: caller invokes retry() with n=3.",
        "confidence": 0.7,
        "tags": ["correctness"],
    }
    base.update(kw)
    return base


def scored(**kw):
    base = dict(
        id="f_001",
        dimension_id="d1",
        dimension_name="Retry semantics",
        file_path="client.py",
        line_start=10,
        line_end=12,
        severity="important",
        title="Retry loop can spin forever",
        body="The loop never decrements the counter.",
        confidence=0.73,
    )
    base.update(kw)
    return ScoredFinding(**base)


def big(seed: str, n: int) -> str:
    """Deterministic filler that pushes a payload past a size threshold."""
    line = seed + " lorem ipsum dolor sit amet consectetur adipiscing elit. "
    return (line * ((n // len(line)) + 1))[:n]


# ---------------------------------------------------------------------------
# Generation
# ---------------------------------------------------------------------------
def main() -> None:
    os.makedirs(TESTDATA, exist_ok=True)
    os.makedirs(os.path.join(FIXTURE_REPO, ".pr-af-context"), exist_ok=True)

    # ---- system prompts (input-independent) --------------------------------
    emit("merge_gate_system", merge_gate._MERGE_GATE_SYSTEM)
    emit("polish_system", polish._POLISH_SYSTEM)

    # ---- intake_phase (ai gate + harness fallback) -------------------------
    prA = pr_data(
        title="Add retry logic to HTTP client",
        description="Wraps the client in a retry decorator with exponential backoff.\nCloses #42.",
        labels=["enhancement", "backend"],
        author="alice",
        commit_messages=["feat: add retry", "test: cover retry", "docs: note retry", "chore: lint", "fix: typo", "extra"],
        # A python majority (client.py + retry.py) makes cluster_changes'
        # primary_language deterministic — max(set(langs), key=langs.count) is
        # hash-order-dependent on an all-distinct-language tie.
        changed_files=[
            changed_file("client.py"),
            changed_file("retry.py", "added"),
            changed_file("client.test.ts", "added"),
            changed_file("README.md"),
        ],
    )
    c = cap(harnesses.intake_phase(prA, depth="deep"))
    sys_intake = next(s for m, s, _ in c if m == "ai")
    emit("intake_gate_system", sys_intake)
    emit("intake_ai_A", next(p for m, _, p in c if m == "ai"))
    emit("intake_fallback_A", next(p for m, _, p in c if m == "harness"))

    prB = pr_data(title="Bump dep", description="")
    c = cap(harnesses.intake_phase(prB, depth="standard"))
    emit("intake_ai_B", next(p for m, _, p in c if m == "ai"))
    emit("intake_fallback_B", next(p for m, _, p in c if m == "harness"))

    # ---- anatomy_phase (empty changed_files -> trivial derivations) --------
    c = cap(harnesses.anatomy_phase(prA, intake_dict(), repo_path=""))
    emit("anatomy_A", next(p for m, _, p in c if m == "harness"))
    c = cap(harnesses.anatomy_phase(prB, intake_dict(pr_summary="Bumps a dependency.", areas_touched=["config"]), repo_path=""))
    emit("anatomy_B", next(p for m, _, p in c if m == "harness"))

    # ---- planning_phase ----------------------------------------------------
    anatA = anatomy_dict(
        clusters=[cluster("cluster_0", "Client core", ["client.py"]), cluster("cluster_1", "Tests", ["client.test.ts"], "typescript")],
        risk_surfaces=["error propagation to callers", "retry storm under load"],
        pr_narrative="Introduces a retry decorator around the HTTP client call path.",
        blast_radius=["caller.py"],
        intent_gaps=["description mentions backoff but code uses fixed sleep"],
        unrelated_changes=["README typo fix"],
        context_notes="Retry count is read from env.",
    )
    c = cap(harnesses.planning_phase(intake_dict(areas_touched=["api", "config"]), anatA, depth="deep", hints=["focus on idempotency", "ignore style"]))
    emit("planning_A", next(p for m, _, p in c if m == "harness"))
    c = cap(harnesses.planning_phase(intake_dict(), anatomy_dict(), depth="standard", hints=[]))
    emit("planning_B", next(p for m, _, p in c if m == "harness"))

    # ---- meta selectors (semantic / mechanical / systemic) -----------------
    diff_patches = {"client.py": "@@ -1,3 +1,5 @@\n+def retry():\n+    pass"}
    for lens, fn in (("semantic", harnesses.meta_semantic), ("mechanical", harnesses.meta_mechanical), ("systemic", harnesses.meta_systemic)):
        c = cap(fn(intake_dict(), anatA, depth="deep", repo_path="", diff_patches=diff_patches, reviewer_feedback="tone down the nitpicks"))
        emit(f"meta_{lens}_A", next(p for m, _, p in c if m == "harness"))
        c = cap(fn(intake_dict(), anatomy_dict(), depth="standard", repo_path="", diff_patches=None, reviewer_feedback=""))
        emit(f"meta_{lens}_B", next(p for m, _, p in c if m == "harness"))
    # C: large-context file-write branch (semantic only; the context_ref logic is shared)
    big_patches = {"client.py": big("patch", 9000)}
    c = cap(harnesses.meta_semantic(intake_dict(), anatA, depth="deep", repo_path=FIXTURE_REPO, diff_patches=big_patches, reviewer_feedback="focus on auth"))
    emit("meta_semantic_C", next(p for m, _, p in c if m == "harness"))

    # ---- review_dimension --------------------------------------------------
    rd_common = dict(
        review_prompt="Verify the retry decorator preserves error types raised by the wrapped call.",
        target_files=["client.py", "retry.py"],
        repo_path="",
    )
    c = cap(harnesses.review_dimension(
        **rd_common,
        context_files=["errors.py"],
        current_depth=0,
        max_depth=2,
        pr_narrative="Adds a retry decorator.",
        risk_surfaces=["error propagation", "timeout handling"],
        intake_summary="Feature PR touching the HTTP client.",
        pr_description="Retries are fail-soft by design because callers have their own fallback.",
        diff_patches={"client.py": "@@ -1 +1 @@\n-x\n+y", "retry.py": "@@ -2 +2 @@\n-a\n+b"},
        all_dimension_names=["Semantic: error paths", "Mechanical: signatures"],
        reviewer_feedback="drop nitpicks, focus on correctness",
        primed_code="1: def retry(fn):\n2:     return fn",
    ))
    emit("review_dimension_A", next(p for m, _, p in c if m == "harness"))
    c = cap(harnesses.review_dimension(
        **rd_common,
        context_files=None,
        current_depth=2,
        max_depth=2,
        pr_narrative="",
        risk_surfaces=None,
        intake_summary="",
        diff_patches=None,
        all_dimension_names=None,
        reviewer_feedback="",
        primed_code="",
    ))
    emit("review_dimension_B", next(p for m, _, p in c if m == "harness"))
    c = cap(harnesses.review_dimension(
        review_prompt="Verify retry preserves error types.",
        target_files=["client.py"],
        repo_path=FIXTURE_REPO,
        context_files=["errors.py"],
        current_depth=0,
        max_depth=2,
        pr_narrative="Adds retry.",
        risk_surfaces=["error propagation"],
        intake_summary="Feature PR.",
        diff_patches={"client.py": big("hunk", 6500)},
        all_dimension_names=["Semantic"],
        reviewer_feedback="",
        primed_code=big("code", 6500),
    ))
    emit("review_dimension_C", next(p for m, _, p in c if m == "harness"))

    # ---- compound_finder_phase --------------------------------------------
    f1 = finding(title="Unbounded retry loop", tags=["correctness"])
    f2 = finding(title="Backoff ignored", file_path="retry.py", line_start=5, tags=["performance"], suggestion="Sleep between attempts.")
    f3 = finding(title="Error type swallowed", file_path="errors.py", severity="critical", suggestion="Re-raise original.")
    ev = {
        "Unbounded retry loop": {
            "primary_code": "def retry(): ...",
            "import_context": "import time",
            "caller_snippets": ["client.call()"],
            "related_code": "config.RETRIES",
            "cross_ref_snippets": ["x=1"],
        }
    }
    c = cap(harnesses.compound_finder_phase([f1, f2, f3], repo_path="", evidence_map=ev))
    emit("compound_finder_A", next(p for m, _, p in c if m == "harness"))
    c = cap(harnesses.compound_finder_phase([f1, f2], repo_path="", evidence_map=None))
    emit("compound_finder_B", next(p for m, _, p in c if m == "harness"))
    bigf = [finding(title="A" * 10, body=big("b", 5000)), finding(title="B" * 10, body=big("c", 5000)), finding(title="Cc", body=big("d", 5000))]
    c = cap(harnesses.compound_finder_phase(bigf, repo_path=FIXTURE_REPO, evidence_map=None))
    emit("compound_finder_C", next(p for m, _, p in c if m == "harness"))

    # ---- post_worthiness_gate ---------------------------------------------
    pw = [
        {"severity": "critical", "file_path": "a.py", "line_start": 3, "title": "Null deref", "body": big("body", 400), "evidence": big("ev", 250)},
        {"severity": "nitpick", "file_path": "b.py", "line_start": 9, "title": "Rename var", "body": "cosmetic", "evidence": ""},
        {"severity": "important", "file_path": "c.py", "line_start": 1, "title": "Race", "body": "shared state", "evidence": "two goroutines"},
    ]
    c = cap(harnesses.post_worthiness_gate(pw))
    emit("post_worthiness_A", next(p for m, _, p in c if m == "harness"))
    c = cap(harnesses.post_worthiness_gate(pw[:2]))
    emit("post_worthiness_B", next(p for m, _, p in c if m == "harness"))

    # ---- compound_dedup_phase ---------------------------------------------
    cd = [
        {"title": "Shared retry gap", "severity": "important", "file_path": "a.py", "tags": ["correctness", "compound"], "body": big("bd", 600), "evidence": big("ed", 400)},
        {"title": "Retry storm", "severity": "critical", "file_path": "b.py", "tags": [], "body": "storm", "evidence": "load"},
    ]
    c = cap(harnesses.compound_dedup_phase(cd, individual_findings_summary="- Unbounded retry loop\n- Backoff ignored"))
    emit("compound_dedup_A", next(p for m, _, p in c if m == "harness"))
    c = cap(harnesses.compound_dedup_phase(cd, individual_findings_summary=""))
    emit("compound_dedup_B", next(p for m, _, p in c if m == "harness"))

    # ---- evidence_verifier -------------------------------------------------
    evpk = {
        "Retry loop can spin forever": {
            "primary_code": "while True: try()",
            "caller_snippets": ["client.call()"],
            "diff_hunk": "@@ -1 +1 @@",
            "import_context": "import time",
            "related_code": "config",
            "cross_ref_snippets": ["r=1"],
        }
    }
    c = cap(harnesses.evidence_verifier([finding()], evidence_packages=evpk, pr_context="PR adds retry.", repo_path=""))
    emit("evidence_verifier_A", next(p for m, _, p in c if m == "harness"))
    c = cap(harnesses.evidence_verifier([finding()], evidence_packages=None, pr_context="", repo_path=""))
    emit("evidence_verifier_B", next(p for m, _, p in c if m == "harness"))
    c = cap(harnesses.evidence_verifier([finding(body=big("bd", 13000))], evidence_packages=None, pr_context="", repo_path=FIXTURE_REPO))
    emit("evidence_verifier_C", next(p for m, _, p in c if m == "harness"))

    # ---- adversary_phase ---------------------------------------------------
    advpk = {
        "Retry loop can spin forever": {
            "primary_code": "while True: try()",
            "caller_snippets": ["client.call()"],
            "diff_hunk": "@@ -1 +1 @@",
            "import_context": "import time",
            "related_code": "config",
        }
    }
    c = cap(harnesses.adversary_phase([finding()], ai_generated_confidence=0.7, pr_context="PR adds retry.", repo_path="", evidence_packages=advpk))
    emit("adversary_A", next(p for m, _, p in c if m == "harness"))
    c = cap(harnesses.adversary_phase([finding()], ai_generated_confidence=0.0, pr_context="", repo_path="", evidence_packages=None))
    emit("adversary_B", next(p for m, _, p in c if m == "harness"))
    c = cap(harnesses.adversary_phase([finding(body=big("bd", 11000))], ai_generated_confidence=0.3, pr_context="", repo_path=FIXTURE_REPO, evidence_packages=None))
    emit("adversary_C", next(p for m, _, p in c if m == "harness"))

    # ---- deepen_findings ---------------------------------------------------
    dp = {"client.py": "@@ -1,2 +1,4 @@\n+def retry():\n+    return call()"}
    c = cap(harnesses.deepen_findings(diff_patches=dp, existing_titles=["Unbounded retry loop", "Backoff ignored"], repo_path="", pr_context="PR adds retry."))
    emit("deepen_A", next(p for m, _, p in c if m == "harness"))
    c = cap(harnesses.deepen_findings(diff_patches=dp, existing_titles=None, repo_path="", pr_context=""))
    emit("deepen_B", next(p for m, _, p in c if m == "harness"))
    c = cap(harnesses.deepen_findings(diff_patches={"client.py": big("hunk", 9500)}, existing_titles=None, repo_path=FIXTURE_REPO, pr_context=""))
    emit("deepen_C", next(p for m, _, p in c if m == "harness"))

    # ---- extract_obligations ----------------------------------------------
    c = cap(harnesses.extract_obligations(diff_patches=dp, repo_path="", pr_context="PR adds retry."))
    emit("obligations_A", next(p for m, _, p in c if m == "harness"))
    c = cap(harnesses.extract_obligations(diff_patches=dp, repo_path="", pr_context=""))
    emit("obligations_B", next(p for m, _, p in c if m == "harness"))
    c = cap(harnesses.extract_obligations(diff_patches={"client.py": big("hunk", 9500)}, repo_path=FIXTURE_REPO, pr_context=""))
    emit("obligations_C", next(p for m, _, p in c if m == "harness"))

    # ---- verify_obligation -------------------------------------------------
    c = cap(harnesses.verify_obligation({"where": "client.py:10 store(key)", "relies_on": "the loader that reads these keys", "property": "the store key equals the lookup key"}, repo_path=""))
    emit("verify_obligation_A", next(p for m, _, p in c if m == "harness"))
    c = cap(harnesses.verify_obligation({"where": "a.py:1 f()", "relies_on": "def of f", "property": "f is pure"}, repo_path=""))
    emit("verify_obligation_B", next(p for m, _, p in c if m == "harness"))

    # ---- coverage_gate -----------------------------------------------------
    covan = anatomy_dict(
        clusters=[cluster("cluster_0", "Client core", ["client.py"]), cluster("cluster_1", "Tests", ["t.py"])],
        risk_surfaces=["error propagation"],
    )
    sys_cov = None
    c = cap(harnesses.coverage_gate(covan, ["cluster_0"], dimension_names_reviewed=["Semantic: error paths", "Mechanical"]))
    sys_cov = next(s for m, s, _ in c if m == "ai")
    emit("coverage_gate_system", sys_cov)
    emit("coverage_gate_A", next(p for m, _, p in c if m == "ai"))
    c = cap(harnesses.coverage_gate(covan, [], dimension_names_reviewed=None))
    emit("coverage_gate_B", next(p for m, _, p in c if m == "ai"))

    # ---- merge_gate user prompt (importable) -------------------------------
    emit("merge_gate_user_A", merge_gate._build_user_prompt(scored(evidence="Trace: A->B->C.", suggestion="Decrement the counter.")))
    emit("merge_gate_user_B", merge_gate._build_user_prompt(scored(evidence="", suggestion=None)))

    # ---- polish user prompt (one-line f-string) ----------------------------
    def polish_user(body: str) -> str:
        return f"Rewrite this PR review comment to be concise and developer-focused.\n\n{body}"

    emit("polish_user_A", polish_user("> [!CAUTION] **Must-fix before merge.**\n\nThe retry loop never terminates. See `client.py:10`."))
    emit("polish_user_B", polish_user("Minor: rename `x` to `retries`."))

    # ---- coverage-gap review prompt (orchestrator._build_gap_dimensions) ---
    def gap_prompt(gap: str) -> str:
        return (
            f"Coverage gap review — this area was missed in the initial review pass.\n\n"
            f"Gap identified: {gap}\n\n"
            f"Inspect the target files with the same depth and rigor as a primary review. "
            f"Look for bugs, logic errors, security issues, and behavioral changes. "
            f"Pay special attention to how this code interacts with the changes that were "
            f"already reviewed in other files — the gap exists because this cluster's "
            f"relationship to the main change wasn't obvious at planning time."
        )

    emit("gap_dimension_A", gap_prompt("The config loader that reads RETRIES was not reviewed."))
    emit("gap_dimension_B", gap_prompt("Untested error path."))

    print("done.")


if __name__ == "__main__":
    main()
