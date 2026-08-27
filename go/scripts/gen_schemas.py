#!/usr/bin/env python3
"""Committed schema-fixture generator for the Go harness port (HIGH-severity fix).

This script is the SINGLE SOURCE OF TRUTH for the JSON-schema fixtures under
``go/internal/harnessx/testdata/schemas/``. It imports the REAL Python pydantic
models that every PR-AF reasoner hands to ``router.app.harness(..., schema=...)``
and emits, for each one, the JSON schema the Go harness uses instead of
reflecting its destination struct with invopop. It begins with
``model_json_schema()`` and applies the two documented runtime-safety
adjustments below.

WHY THIS EXISTS
---------------
The Go SDK (pinned ``sdk/go`` in ``go/go.mod``) validates every parsed harness
output against the schema map with a strict JSON-Schema validator
(``santhosh-tekuri/jsonschema/v5`` in ``harness/schema.go`` ->
``validateAgainstSchema``) and drives its schema-retry loop off validation
failures. The Go port previously fed that validator an invopop-reflected schema,
which marks EVERY field required, renders pointer fields (``suggestion``,
``hidden_trap``) non-nullable, and sets ``additionalProperties: false``. Pydantic
instead makes defaulted fields optional, ``X | None`` fields nullable, and
ignores extra keys — so Python-valid model output (omitting
``tags``/``confidence``/``evidence``, ``"suggestion": null``, extra explanatory
keys) was REJECTED by the Go node: wasted retries, fallback outputs, lost
findings. Embedding the pydantic schema restores real parity on those three axes.

DELIBERATE DEVIATION 1: SEVERITY
--------------------------------
``schemas/severity.py`` types finding-severity as
``Annotated[Literal[...], BeforeValidator(normalize_severity)]``.
``model_json_schema()`` therefore advertises a strict 4-value ``enum``, but the
``BeforeValidator`` COERCES synonyms (``"high"`` -> ``"important"``) before
validation and NEVER rejects. Python's runtime validation (``model_validate``)
is pydantic, not JSON-Schema, so it honours the coercion. The Go SDK can only
run JSON-Schema validation, which cannot express a BeforeValidator — a strict
enum there would REJECT ``"high"`` (the exact "swallowed review" incident
``severity.py`` documents) and would be stricter than BOTH Python's runtime AND
the prior invopop schema (Go's ``type Severity string`` advertised no enum at
all). So we strip the ``enum`` keyword from Severity nodes, leaving
``type: string``; Go's ``schemas.Severity.UnmarshalJSON`` then normalizes exactly
like the BeforeValidator. This divergence follows Python's true runtime
behaviour rather than its static schema.

DELIBERATE DEVIATION 2: EXPLICIT RESULT COLLECTIONS
---------------------------------------------------
Several pydantic result models default their top-level collection to an empty
list, making ``{}`` schema-valid even though it is indistinguishable from a
provider or parser no-op. The Go review contract requires those collection keys
to be present while still allowing explicit empty arrays. This preserves clean
results such as ``{"findings": []}`` and rejects ambiguous empty objects.

REPRODUCE (from the pr-af repo root):
  /tmp/claude-1000/-home-abir-gb/e0447ca2-28f4-49fe-ae8a-ead45bdad68c/scratchpad/praf-venv/bin/python go/scripts/gen_schemas.py

Or with any interpreter that has pr-af installed / on PYTHONPATH=src:
  PYTHONPATH=src python go/scripts/gen_schemas.py

Deterministic and idempotent: rerunning overwrites the fixtures with identical
bytes unless a Python model changed — exactly the signal the Go drift test
exists to catch.
"""

from __future__ import annotations

import json
import os
import sys
from typing import Any

# Make `pr_af` importable when run from the repo root without install.
_REPO_ROOT = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
_SRC = os.path.join(_REPO_ROOT, "src")
if os.path.isdir(_SRC) and _SRC not in sys.path:
    sys.path.insert(0, _SRC)

from pr_af.reasoners import harnesses  # noqa: E402
from pr_af.schemas import pipeline  # noqa: E402
from pr_af.schemas.severity import VALID_SEVERITIES  # noqa: E402

TESTDATA = os.path.join(
    os.path.dirname(os.path.dirname(os.path.abspath(__file__))),
    "internal",
    "harnessx",
    "testdata",
    "schemas",
)

# fixture basename (NO leading underscore — go:embed skips names starting with
# "_" unless the pattern uses the all: prefix) -> the exact pydantic model the
# matching reasoner passes to router.app.harness(schema=...). Each entry maps 1:1
# to a Go destination type used with harnessx.Run[T]; see schemas_registry.go.
MODELS: dict[str, Any] = {
    # private harness-result models (reasoners/harnesses.py, module scope)
    "AdversaryPhaseResult": harnesses._AdversaryPhaseResult,
    "AnatomySemanticResult": harnesses._AnatomySemanticResult,
    "CompoundDedupResult": harnesses._CompoundDedupResult,
    "CompoundResult": harnesses._CompoundResult,
    "DeepenResult": harnesses._DeepenResult,
    "ObligationVerdict": harnesses._ObligationVerdict,
    "ObligationsResult": harnesses._ObligationsResult,
    "PostWorthinessResult": harnesses._PostWorthinessResult,
    "ReviewFindingsResult": harnesses._ReviewFindingsResult,
    "VerificationResult": harnesses._VerificationResult,
    # public pipeline models (schemas/pipeline.py)
    "IntakeResult": pipeline.IntakeResult,
    "MetaDimensionResult": pipeline.MetaDimensionResult,
    "ReviewPlan": pipeline.ReviewPlan,
}

_SEVERITY_ENUM = list(VALID_SEVERITIES)

# These collection fields may be empty, but they must be explicit. Without the
# requirement, `{}` is schema-valid and is indistinguishable from a reasoner
# that intentionally returned an empty analysis.
_REQUIRED_COLLECTION_OUTPUTS: dict[str, list[str]] = {
    "AdversaryPhaseResult": ["results"],
    "CompoundResult": ["findings"],
    "ObligationsResult": ["obligations"],
    "ReviewFindingsResult": ["findings"],
    "VerificationResult": ["verified_findings"],
}


def _relax_severity_enums(node: Any) -> Any:
    """Strip the strict Severity ``enum`` while leaving every other keyword intact.

    See the module docstring: the ``enum`` is the one place ``model_json_schema()``
    over-constrains relative to Python's actual (BeforeValidator-normalized)
    runtime validation. We recurse first, then drop ``enum`` from any node that is
    exactly the canonical 4-value string severity so the relaxation is surgical
    (a plain ``str`` field with an unrelated enum would be left untouched — none
    exist today, but the guard keeps this honest).
    """
    if isinstance(node, dict):
        out = {k: _relax_severity_enums(v) for k, v in node.items()}
        if out.get("type") == "string" and out.get("enum") == _SEVERITY_ENUM:
            out.pop("enum", None)
        return out
    if isinstance(node, list):
        return [_relax_severity_enums(x) for x in node]
    return node


def emit(name: str, model: Any) -> None:
    schema = _relax_severity_enums(model.model_json_schema())
    if required := _REQUIRED_COLLECTION_OUTPUTS.get(name):
        schema["required"] = required
    # sort_keys makes the committed fixture diff-stable; the Go SDK re-marshals
    # the map (json.MarshalIndent sorts map keys anyway) so on-disk key order has
    # no runtime effect. Trailing newline keeps gofmt/editors happy.
    text = json.dumps(schema, indent=2, sort_keys=True) + "\n"
    path = os.path.join(TESTDATA, name + ".json")
    with open(path, "w", encoding="utf-8") as f:
        f.write(text)
    print(f"  wrote {name}.json ({len(text)} bytes)")


def main() -> None:
    os.makedirs(TESTDATA, exist_ok=True)
    for name in sorted(MODELS):
        emit(name, MODELS[name])
    print(f"done. {len(MODELS)} schema fixtures.")


if __name__ == "__main__":
    main()
