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
"""agent-result.json: the run result ACK Autopilot's Dispatcher reads.

Schema ack.autopilot.result/v1 (the Go reader is
prow/jobs/tools/cmd/command/autopilot/dispatcher/result.go; both sides test
against dispatcher/testdata/agent-result-v1.json).

prow-job.sh creates the file before anything can fail, so a job that dies
early reports failed/JOB_INCOMPLETE. The harness adds tokens, iterations, the
e2e result and any failure reason. prow-job.sh records the final outcome on
each exit path. Every value is checked here, and the Dispatcher checks again,
since it trusts nothing a run reports.

The file lives in $ARTIFACTS, which Prow uploads with the job's artifacts.
Standard library only: prow-job.sh runs this file directly.
"""

from __future__ import annotations

import argparse
import json
import os
import re
import sys
import tempfile
import unicodedata
from pathlib import Path

SCHEMA = "ack.autopilot.result/v1"
# Every workflow that writes a result: the three role workflows, sdk-bump
# (Plan 6, written by Go: ack-build-tools autopilot sdk-bump,
# autopilot/sdkbump/result.go, with the same bytes;
# tests/test_roles_offline.py checks its golden file here) and triage (Plan 8).
WORKFLOWS = ("add-resource", "add-field", "address-feedback", "sdk-bump", "triage")
# Design 4.1 outcomes a run can report, plus pr_opened (a run that left a PR
# for review) and pr_updated (address-feedback pushed to an existing PR).
OUTCOMES = frozenset({
    "pr_opened",
    "pr_updated",
    "completed_no_change",
    "needs_human",
    "failed",
    "wont_fix:not_real",
    "wont_fix:aws_api_blocker",
    "wont_fix:env_blocker",
    "wont_fix:product_decision",
})
# roles/e2e.py E2EResult.status values.
E2E_STATUSES = frozenset({"PASS", "FAIL", "SKIPPED", "NOT_RUN", "ERROR", "NOT_NEEDED"})
# Prow's default when ARTIFACTS is unset; roles/e2e.py uses the same.
DEFAULT_ARTIFACTS = "/tmp/ack-test-logs"

_REASON_RE = re.compile(r"^[A-Z][A-Z0-9_]{0,63}$")
_PR_RE = re.compile(r"^https://github\.com/[A-Za-z0-9-]+/[A-Za-z0-9._-]+/pull/[1-9][0-9]*$")
_WORK_ITEM_RE = re.compile(r"^sha256:[0-9a-f]{16}$")
_PROW_JOB_ID_RE = re.compile(r"^[A-Za-z0-9._-]{0,128}$")
# (group, keys, max): the bounds match the Go reader's, which refuses a file
# past them so a run can never overflow the ledger's running totals.
_MAX_TOKENS = 2**40
_MAX_COUNT = 1000
_COUNTS = (
    ("tokens", ("input", "output", "total"), _MAX_TOKENS),
    ("iterations", ("impl", "replan"), _MAX_COUNT),
    ("e2e", ("attempts",), _MAX_COUNT),
)
# The v1 keys. The Go reader ignores unknown keys; this writer refuses them so
# a typo or a stray value never reaches the file.
_KEYS = frozenset({
    "schema", "workflow", "work_item", "prow_job_id", "outcome", "reason", "pr",
    "tokens", "iterations", "e2e", "triage",
})
_NESTED_KEYS = {
    "tokens": frozenset({"input", "output", "total"}),
    "iterations": frozenset({"impl", "replan"}),
    "e2e": frozenset({"status", "attempts"}),
}

# The triage workflow's verdict (Plan 8; design 5.1): one closed-enum decision
# for one ledger item. The Go reader is dispatcher.ParseTriage; both sides test
# against dispatcher/testdata/agent-result-triage-v1.json.
TRIAGE_SCHEMA = "ack.autopilot.triage/v1"
TRIAGE_MODES = ("api",)
TRIAGE_DECISIONS = ("resource", "field-config", "field-hook", "not-worth-modelling", "undecided")
# The change class each decision asks for; the other two ask for none.
TRIAGE_DECISION_CLASS = {
    "resource": "new-resource",
    "field-config": "config-only",
    "field-hook": "hand-written-code",
}
# UTF-8 bytes, the same bound as ledger.MaxTriageReason.
TRIAGE_MAX_REASON = 600
TRIAGE_MAX_REFS = 16
# An evidence reference: an SDK operation, shape or member path, or a CRD path
# under spec or status. Each must exist; triage_evidence checks that.
TRIAGE_REF_RE = re.compile(
    r"^(?:(?:op|shape|field):[A-Za-z0-9_][A-Za-z0-9_.]{0,199}"
    r"|crd:(?:spec|status)\.[A-Za-z0-9_][A-Za-z0-9_.]{0,199})$"
)
# Why an answer became undecided: a SCREAMING_SNAKE code and, for some codes,
# the offending reference.
TRIAGE_INVALID_RE = re.compile(r"^[A-Z][A-Z0-9_]{0,63}(?: [A-Za-z0-9_.:-]{1,210})?$")
_TRIAGE_KEYS = frozenset({"schema", "mode", "items"})
_TRIAGE_ITEM_KEYS = frozenset({"item_key", "decision", "class", "reason", "evidence_refs", "invalid"})
_TRIAGE_REQUIRED = ("item_key", "decision", "reason", "evidence_refs", "invalid")


def plain_text(value: object, limit: int = TRIAGE_MAX_REASON) -> bool:
    """One line of text: no control characters or surrogates, at most limit UTF-8 bytes."""
    if not isinstance(value, str):
        return False
    if any(unicodedata.category(c) in ("Cc", "Cs") for c in value):
        return False
    return len(value.encode("utf-8")) <= limit


def validate_triage(t: object, work_item: object) -> list[str]:
    """Return one problem per invalid part of a triage verdict. Never raises."""
    if not isinstance(t, dict):
        return ["triage must be an object"]
    problems: list[str] = []
    unknown = sorted(str(k) for k in t if k not in _TRIAGE_KEYS)
    if unknown:
        problems.append("unknown triage keys: " + ", ".join(unknown))
    if t.get("schema") != TRIAGE_SCHEMA:
        problems.append(f"triage.schema must be {TRIAGE_SCHEMA}")
    if not _one_of(t.get("mode"), TRIAGE_MODES):
        problems.append("triage.mode must be one of " + ", ".join(TRIAGE_MODES))
    items = t.get("items")
    if not isinstance(items, list) or len(items) != 1 or not isinstance(items[0], dict):
        problems.append("triage.items must hold exactly one object")
        return problems
    it = items[0]
    unknown = sorted(str(k) for k in it if k not in _TRIAGE_ITEM_KEYS)
    if unknown:
        problems.append("unknown triage item keys: " + ", ".join(unknown))
    for key in _TRIAGE_REQUIRED:
        if key not in it:
            problems.append(f"triage item needs {key}")
    if not isinstance(work_item, str) or not work_item or it.get("item_key") != work_item:
        problems.append("triage item_key must equal work_item")
    decision = it.get("decision")
    if not _one_of(decision, TRIAGE_DECISIONS):
        problems.append("triage decision must be one of " + ", ".join(TRIAGE_DECISIONS))
    else:
        want = TRIAGE_DECISION_CLASS.get(decision)
        if ("class" in it) != (want is not None) or it.get("class") != want:
            problems.append("triage class must be the decision's class (absent for not-worth-modelling and undecided)")
    if "reason" in it and not plain_text(it.get("reason")):
        problems.append(f"triage reason must be one line of plain text, at most {TRIAGE_MAX_REASON} bytes")
    refs = it.get("evidence_refs")
    if "evidence_refs" in it and (
        not isinstance(refs, list) or len(refs) > TRIAGE_MAX_REFS
        or not all(isinstance(r, str) and TRIAGE_REF_RE.fullmatch(r) for r in refs)
    ):
        problems.append(f"triage evidence_refs must be at most {TRIAGE_MAX_REFS} op:/shape:/field:/crd: references")
    invalid = it.get("invalid")
    if "invalid" in it:
        if (not isinstance(invalid, list) or len(invalid) > TRIAGE_MAX_REFS
                or not all(isinstance(c, str) and TRIAGE_INVALID_RE.fullmatch(c) for c in invalid)):
            problems.append(f"triage invalid must be at most {TRIAGE_MAX_REFS} SCREAMING_SNAKE codes")
        elif invalid and decision != "undecided":
            problems.append("a triage item with invalid entries must be undecided")
    return problems


def result_path() -> Path:
    """AGENT_RESULT_FILE, else agent-result.json in $ARTIFACTS."""
    explicit = os.environ.get("AGENT_RESULT_FILE")
    if explicit:
        return Path(explicit)
    return Path(os.environ.get("ARTIFACTS") or DEFAULT_ARTIFACTS) / "agent-result.json"


def new(workflow: str, work_item: str = "") -> dict:
    """The file prow-job.sh creates: a failed run until something says otherwise."""
    return {
        "schema": SCHEMA,
        "workflow": workflow,
        "work_item": work_item,
        "prow_job_id": os.environ.get("PROW_JOB_ID", ""),
        "outcome": "failed",
        "reason": "JOB_INCOMPLETE",
        "pr": "",
        "tokens": {"input": 0, "output": 0, "total": 0},
        "iterations": {"impl": 0, "replan": 0},
        "e2e": {"status": "NOT_RUN", "attempts": 0},
    }


def _is_count(v: object) -> bool:
    return isinstance(v, int) and not isinstance(v, bool) and v >= 0


def _one_of(v: object, allowed) -> bool:
    # isinstance first: an unhashable value (a list, a dict) would make the
    # set membership test raise TypeError.
    return isinstance(v, str) and v in allowed


def validate(doc: dict) -> list[str]:
    """Return one problem per invalid value. Never raises."""
    if not isinstance(doc, dict):
        return ["result must be a JSON object"]
    problems: list[str] = []
    unknown = sorted(str(k) for k in doc if k not in _KEYS)
    if unknown:
        problems.append("unknown keys: " + ", ".join(unknown))
    if doc.get("schema") != SCHEMA:
        problems.append(f"schema must be {SCHEMA}")
    if not _one_of(doc.get("workflow"), WORKFLOWS):
        problems.append("workflow must be one of " + ", ".join(WORKFLOWS))
    work_item = doc.get("work_item")
    if not isinstance(work_item, str) or (work_item and not _WORK_ITEM_RE.fullmatch(work_item)):
        problems.append("work_item must be empty or sha256:<16 hex digits>")
    prow_job_id = doc.get("prow_job_id")
    if not isinstance(prow_job_id, str) or not _PROW_JOB_ID_RE.fullmatch(prow_job_id):
        problems.append("prow_job_id must be at most 128 of [A-Za-z0-9._-]")
    outcome = doc.get("outcome")
    if not _one_of(outcome, OUTCOMES):
        problems.append("outcome must be one of " + ", ".join(sorted(OUTCOMES)))
    reason = doc.get("reason")
    if not isinstance(reason, str) or (reason and not _REASON_RE.fullmatch(reason)):
        problems.append("reason must be empty or SCREAMING_SNAKE (at most 64)")
    pr = doc.get("pr")
    if not isinstance(pr, str) or (pr and not _PR_RE.fullmatch(pr)):
        problems.append("pr must be empty or https://github.com/<owner>/<repo>/pull/<n>")
    elif outcome in ("pr_opened", "pr_updated") and not pr:
        problems.append(f"outcome {outcome} needs a pr")
    for group, keys, limit in _COUNTS:
        values = doc.get(group)
        if not isinstance(values, dict):
            problems.append(f"{group} must be an object")
            continue
        unknown = sorted(str(k) for k in values if k not in _NESTED_KEYS[group])
        if unknown:
            problems.append(f"unknown {group} keys: " + ", ".join(unknown))
        for key in keys:
            value = values.get(key)
            if not _is_count(value):
                problems.append(f"{group}.{key} must be a non-negative integer")
            elif value > limit:
                problems.append(f"{group}.{key} out of range (max {limit})")
    e2e = doc.get("e2e")
    if isinstance(e2e, dict) and not _one_of(e2e.get("status"), E2E_STATUSES):
        problems.append("e2e.status must be one of " + ", ".join(sorted(E2E_STATUSES)))
    if "triage" in doc:
        if doc.get("workflow") != "triage":
            problems.append("only the triage workflow records a triage verdict")
        problems.extend(validate_triage(doc["triage"], doc.get("work_item")))
    elif doc.get("workflow") == "triage" and outcome == "completed_no_change":
        problems.append("a completed triage run needs a triage verdict")
    return problems


def write(doc: dict, path: Path | None = None) -> None:
    """Validate doc and replace the file atomically; raise ValueError if invalid."""
    problems = validate(doc)
    if problems:
        raise ValueError("; ".join(problems))
    path = path or result_path()
    path.parent.mkdir(parents=True, exist_ok=True)
    fd, tmp = tempfile.mkstemp(dir=path.parent, prefix=".agent-result.")
    try:
        # mkstemp creates 0600; the Prow upload sidecar may run as another uid.
        os.fchmod(fd, 0o644)
        with os.fdopen(fd, "w") as f:
            json.dump(doc, f, indent=2, sort_keys=True)
            f.write("\n")
        os.replace(tmp, path)
    except BaseException:
        Path(tmp).unlink(missing_ok=True)
        raise


def update(path: Path | None = None, **fields) -> dict:
    """Change fields of the existing file. A dict value is merged into the
    nested object of the same name, so e2e={"attempts": 2} keeps e2e.status.
    Raise ValueError if the file is not a JSON object or the result is invalid."""
    path = path or result_path()
    doc = json.loads(path.read_text())  # JSONDecodeError is a ValueError
    if not isinstance(doc, dict):
        raise ValueError(f"{path} does not hold a JSON object")
    for key, value in fields.items():
        if isinstance(value, dict) and isinstance(doc.get(key), dict):
            doc[key] = {**doc[key], **value}
        else:
            doc[key] = value
    write(doc, path)
    return doc


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="Write agent-result.json for ACK Autopilot")
    sub = parser.add_subparsers(dest="command", required=True)
    init = sub.add_parser("init", help="create the file: failed/JOB_INCOMPLETE")
    init.add_argument("--workflow", required=True)
    init.add_argument("--work-item", default="")
    setp = sub.add_parser("set", help="change the given fields of the existing file")
    setp.add_argument("--outcome")
    setp.add_argument("--reason")
    setp.add_argument("--pr")
    setp.add_argument("--work-item")
    args = parser.parse_args(argv)
    try:
        if args.command == "init":
            write(new(args.workflow, args.work_item))
        else:
            fields = {
                name: value
                for name, value in (
                    ("outcome", args.outcome),
                    ("reason", args.reason),
                    ("pr", args.pr),
                    ("work_item", args.work_item),
                )
                if value is not None
            }
            # A new outcome clears the old reason unless one is given, so a
            # stale JOB_INCOMPLETE never survives an outcome change.
            if args.outcome is not None and args.reason is None:
                fields["reason"] = ""
            update(**fields)
    except (OSError, ValueError) as exc:
        print(f"result.py: {exc}", file=sys.stderr)
        return 2
    return 0


if __name__ == "__main__":
    sys.exit(main())
