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
import sys
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


MODEL_URL = "https://raw.githubusercontent.com/aws/aws-sdk-go-v2/{sdk_to}/codegen/sdk-codegen/aws-models/{model}.json"


def _file_call(path) -> ModelCall:
    """A canned answer instead of Bedrock, for offline runs. triage-job.sh
    passes it only when TRIAGE_MODEL_RESPONSE_FILE is set, which the
    workflow definition never does."""
    text = path.read_text()
    return lambda system, user: (text, dict(_NO_TOKENS))


def _bedrock_call(model_id: str) -> ModelCall:
    from utils.bedrock import converse_text

    return lambda system, user: converse_text(system, user, model_id=model_id)


def _record(outcome: str, reason: str, **fields) -> int:
    """Record the run's outcome; 2 when the result file cannot be written."""
    try:
        result.update(outcome=outcome, reason=reason, **fields)
    except (OSError, ValueError) as exc:
        print(f"triage: could not record {outcome} {reason}: {exc}", file=sys.stderr)
        return 2
    return 0 if outcome == "completed_no_change" else 1


def main(argv: list[str] | None = None) -> int:
    import argparse
    from pathlib import Path

    import yaml

    from config.defaults import DEFAULT_MODEL_ID
    from workflows import validation
    from workflows.triage_evidence import Item, Model, build, generator_config, model_name

    parser = argparse.ArgumentParser(description="ACK Autopilot triage, mode api (Plan 8)")
    sub = parser.add_subparsers(dest="command", required=True)
    url = sub.add_parser("model-url", help="print the SDK model URL for --sdk-to")
    url.add_argument("--service", required=True)
    url.add_argument("--sdk-to", required=True)
    url.add_argument("--controller-dir", required=True)
    run = sub.add_parser("run", help="triage one item and update agent-result.json")
    for name in ("service", "resource", "field", "finding-class", "detector-work", "change-class",
                 "sdk-to", "work-item", "controller-dir", "sdk-model"):
        run.add_argument("--" + name, required=True)
    run.add_argument("--model", default=DEFAULT_MODEL_ID)
    run.add_argument("--model-response-file")
    args = parser.parse_args(argv)

    if args.command == "model-url":
        if not validation._SERVICE_RE.fullmatch(args.service) or not validation.SDK_TO_RE.fullmatch(args.sdk_to):
            print("triage: model-url needs a service alias and service/<package>/vX.Y.Z", file=sys.stderr)
            return 2
        cfg = generator_config(Path(args.controller_dir))
        print(MODEL_URL.format(sdk_to=args.sdk_to, model=model_name(cfg, args.service)))
        return 0

    item = Item(service=args.service, resource=args.resource, field=args.field,
                finding_class=args.finding_class, detector_work=args.detector_work,
                change_class=args.change_class, sdk_to=args.sdk_to, work_item=args.work_item)
    problems = validation.validate_triage_inputs(
        service=item.service, resource=item.resource, field=item.field, finding_class=item.finding_class,
        detector_work=item.detector_work, change_class=item.change_class, sdk_to=item.sdk_to,
        work_item=item.work_item, model=args.model)
    if problems:
        print("triage: " + "; ".join(problems), file=sys.stderr)
        return _record("needs_human", "INVALID_INPUT")
    try:
        model = Model.load(Path(args.sdk_model))
    except (OSError, ValueError) as exc:
        print(f"triage: SDK model: {exc}", file=sys.stderr)
        return _record("needs_human", "SDK_MODEL_UNREADABLE")
    try:
        pack = build(item, model, Path(args.controller_dir))
    except (OSError, ValueError, yaml.YAMLError) as exc:
        print(f"triage: controller: {exc}", file=sys.stderr)
        return _record("needs_human", "CONTROLLER_UNREADABLE")
    call = _file_call(Path(args.model_response_file)) if args.model_response_file else _bedrock_call(args.model)
    try:
        out = triage(pack, call)
    except Exception as exc:  # noqa: BLE001 - any failure of the call is a failed run
        print(f"triage: model call: {type(exc).__name__}: {exc}", file=sys.stderr)
        return _record("failed", "MODEL_CALL_FAILED")
    it = out.doc["items"][0]
    print(f"triage {item.work_item} decision={it['decision']} invalid={','.join(it['invalid']) or '-'} "
          f"called={str(out.called).lower()} tokens={out.tokens['total']}")
    return _record("completed_no_change", "TRIAGE_VERDICT", triage=out.doc, tokens=out.tokens)


if __name__ == "__main__":
    sys.exit(main())
