# Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
#
# Licensed under the Apache License, Version 2.0 (the "License"). You may not use this file except in compliance
# with the License. A copy of the License is located at
#
#    http://www.apache.org/licenses/LICENSE-2.0
#
# or in the 'license' file accompanying this file. This file is distributed on an 'AS IS' BASIS, WITHOUT WARRANTIES
# OR CONDITIONS OF ANY KIND, express or implied. See the License for the specific language governing permissions
# and limitations under the License.
"""ACK Autopilot triage workflow, mode api (Plan 8; design 5.1, 5.2).

One needs-config ledger item in; one decision out, in agent-result.json under
"triage" (ack.autopilot.triage/v1). The model call has no tools (one Bedrock
Converse request without toolConfig); its input frames every text that is not
computed by code as untrusted; its output is parsed strictly and every name it
cites is checked against the SDK model and the CRD. Anything that fails a
check turns the answer into "undecided": a maintainer decides. The Dispatcher
checks the verdict again and applies the stricter-only rule. Nothing here
changes a repository, the ledger or GitHub.
"""

from __future__ import annotations

import json
import re
import unicodedata
from dataclasses import dataclass
from typing import Callable

from workflows import result
from workflows.triage_evidence import REF_RE, EvidencePack
from workflows.triage_prompt import SYSTEM_PROMPT, user_prompt

MODE = "api"
MAX_RESPONSE_CHARS = 8000
_ANSWER_KEYS = frozenset({"decision", "reason", "evidence_refs"})
_FENCE_RE = re.compile(r"^```(?:json)?\s*\n(.*)\n```$", re.DOTALL)
_NO_TOKENS = {"input": 0, "output": 0, "total": 0}

# (system prompt, user prompt) -> (answer text, {"input", "output", "total"})
ModelCall = Callable[[str, str], "tuple[str, dict]"]


@dataclass
class Triaged:
    doc: dict
    tokens: dict
    called: bool


def clean_reason(value: object) -> str:
    """One line of plain text, at most TRIAGE_MAX_REASON UTF-8 bytes."""
    if not isinstance(value, str):
        return ""
    chars = []
    for c in value:
        category = unicodedata.category(c)
        if category == "Cs":
            continue
        chars.append(" " if category == "Cc" else c)
    text = " ".join("".join(chars).split())
    return text.encode("utf-8")[: result.TRIAGE_MAX_REASON].decode("utf-8", "ignore").strip()


def parse_answer(text: object) -> tuple[dict | None, str]:
    """The answer as a JSON object, or None and why not."""
    if not isinstance(text, str) or len(text) > MAX_RESPONSE_CHARS:
        return None, "RESPONSE_TOO_LONG"
    body = text.strip()
    fenced = _FENCE_RE.match(body)
    if fenced:
        body = fenced.group(1).strip()
    try:
        doc = json.loads(body)
    except ValueError:
        return None, "INVALID_JSON"
    if not isinstance(doc, dict):
        return None, "NOT_AN_OBJECT"
    return doc, ""


def check_answer(doc: dict | None, problem: str, pack: EvidencePack) -> dict:
    """The verdict item for one answer. Every failed check is an invalid code
    and makes the decision undecided."""
    invalid = [problem] if problem else []
    decision, reason, refs = "undecided", "", []
    if doc is not None:
        if set(doc) - _ANSWER_KEYS:
            invalid.append("UNKNOWN_KEYS")
        d = doc.get("decision")
        if isinstance(d, str) and d in result.TRIAGE_DECISIONS:
            decision = d
        else:
            invalid.append("UNKNOWN_DECISION")
        reason = clean_reason(doc.get("reason"))
        raw = doc.get("evidence_refs", [])
        if not isinstance(raw, list) or len(raw) > result.TRIAGE_MAX_REFS:
            invalid.append("BAD_EVIDENCE_REFS")
            raw = []
        for ref in raw:
            if not isinstance(ref, str) or not REF_RE.fullmatch(ref):
                invalid.append("MALFORMED_REF")
            elif not pack.ref_exists(ref):
                invalid.append(f"UNKNOWN_REF {ref}")
            elif ref not in refs:
                refs.append(ref)
        if decision == "resource" and not any(r.startswith("op:") for r in refs):
            invalid.append("RESOURCE_WITHOUT_OPERATION")
        if pack.item.detector_work == "server-default" and decision in ("resource", "not-worth-modelling"):
            invalid.append("DECISION_NOT_ALLOWED_FOR_SERVER_DEFAULT")
    invalid = sorted(set(invalid))[: result.TRIAGE_MAX_REFS]
    if invalid:
        decision = "undecided"
    item = {
        "item_key": pack.item.work_item,
        "decision": decision,
        "reason": reason,
        "evidence_refs": sorted(refs),
        "invalid": invalid,
    }
    if decision in result.TRIAGE_DECISION_CLASS:
        item["class"] = result.TRIAGE_DECISION_CLASS[decision]
    return item


def _doc(item: dict) -> dict:
    return {"schema": result.TRIAGE_SCHEMA, "mode": MODE, "items": [item]}


def triage(pack: EvidencePack, call: ModelCall) -> Triaged:
    """Triage one item. A field the SDK model does not have is undecided
    without a model call: the detector's finding and the model disagree."""
    if not pack.field_found:
        item = {
            "item_key": pack.item.work_item, "decision": "undecided", "reason": "",
            "evidence_refs": [], "invalid": ["ITEM_NOT_IN_MODEL"],
        }
        return Triaged(_doc(item), dict(_NO_TOKENS), False)
    text, tokens = call(SYSTEM_PROMPT, user_prompt(pack))
    doc, problem = parse_answer(text)
    return Triaged(_doc(check_answer(doc, problem, pack)), tokens, True)
