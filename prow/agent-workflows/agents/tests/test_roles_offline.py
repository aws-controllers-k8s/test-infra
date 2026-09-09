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
"""Offline tests for the role-based add-resource harness — no AWS, no Bedrock.

Validates the deterministic harness logic: the verdict parser, the E2E output
classifier, test_config templating, the Phase-4 reporting distinctions, the
progress reporter, and — most importantly — every graph edge condition against
fake GraphStates, asserting the loop bounds and that exactly one plan-exit edge
fires after plan review.

Run from the agents package dir:
    .venv/bin/python tests/test_roles_offline.py
"""

from __future__ import annotations

import io
import os
import sys
import tempfile
import shutil as _sh
from dataclasses import dataclass, field
from pathlib import Path

import yaml

# Make the agents package dir importable as the top-level source root.
_PKG_ROOT = Path(__file__).resolve().parents[1]
if str(_PKG_ROOT) not in sys.path:
    sys.path.insert(0, str(_PKG_ROOT))

from roles import graph as gm
from roles.config import Config
from roles.e2e import _classify, _to_snake, ensure_test_config
from roles.verdict import Decision, parse_decision

# ack-dev-skills is a peer of test-infra in the ACK workspace. _PKG_ROOT is
# .../aws-controllers-k8s/test-infra/prow/agent-workflows/agents, so the ACK
# workspace root (which holds ack-dev-skills) is parents[3].
SKILLS = _PKG_ROOT.parents[3] / "ack-dev-skills"


# --------------------------------------------------------------------------
# Fakes mimicking the Strands GraphState surface used by the edge conditions:
# .results[node_id].get_agent_results() and .execution_order[].node_id
# --------------------------------------------------------------------------
@dataclass
class FakeAgentResult:
    text: str

    def __str__(self) -> str:
        return self.text


@dataclass
class FakeNodeResult:
    text: str

    def get_agent_results(self):
        return [FakeAgentResult(self.text)] if self.text else []


@dataclass
class FakeNode:
    node_id: str


@dataclass
class FakeState:
    results: dict = field(default_factory=dict)
    execution_order: list = field(default_factory=list)

    def set(self, node_id: str, text: str):
        self.results[node_id] = FakeNodeResult(text)
        self.execution_order.append(FakeNode(node_id))
        return self


INV = {gm.KEY_MAX_REPLAN: 1, gm.KEY_MAX_IMPL: 4}


def check(name, got, want):
    status = "ok" if got == want else "FAIL"
    print(f"  [{status}] {name}: got={got!r} want={want!r}")
    assert got == want, f"{name}: {got!r} != {want!r}"


def test_verdict():
    print("verdict parser:")
    check("approve header", parse_decision("## Decision: APPROVE\n..."), Decision.APPROVE)
    check("revise header", parse_decision("## Decision: REVISE"), Decision.REVISE)
    check("approve bold", parse_decision("Decision: **APPROVE**"), Decision.APPROVE)
    check("empty -> revise", parse_decision(""), Decision.REVISE)
    check("ambiguous prose -> revise", parse_decision("we should revise and then approve"), Decision.REVISE)
    check("plain approved", parse_decision("Looks good. APPROVED."), Decision.APPROVE)


def test_snake():
    print("snake_case:")
    check("BackupVault", _to_snake("BackupVault"), "backup_vault")
    check("Repository", _to_snake("Repository"), "repository")
    check("RepositoryCreationTemplate", _to_snake("RepositoryCreationTemplate"), "repository_creation_template")


def test_e2e_classify():
    print("e2e classify:")
    check("passed", _classify("== 3 passed in 12s ==", 0), "PASS")
    check("failed rc", _classify("== 1 failed, 2 passed ==", 1), "FAIL")
    check("skipped only", _classify("== 2 skipped ==", 0), "SKIPPED")
    check("skipped+passed -> pass", _classify("1 passed, 1 skipped", 0), "PASS")
    # pytest colorizes its summary; the color escape leaves a letter right before
    # the digit ("\x1b[32m2 passed"), which defeated the \b in the passed regex
    # and made a green run classify FAIL. Regression: strip ANSI before matching.
    check("ansi passed -> pass",
          _classify("==== \x1b[32m2 passed\x1b[0m, \x1b[33m34 warnings\x1b[0m in 63s ====", 0),
          "PASS")


def test_conditions():
    print("graph edge conditions:")
    # Initial plan, reviewer says REVISE: re-plan allowed (0 used < 1).
    s = FakeState().set(gm.PLANNER, "plan v1").set(gm.PLAN_REVIEWER, "## Decision: REVISE")
    check("replan after 1st REVISE", gm.cond_replan(s, invocation_state=INV), True)
    check("plan_done blocked while replan", gm.cond_plan_done(s, invocation_state=INV), False)

    # Re-plan happened (planner ran twice), reviewer still REVISE: budget spent.
    s2 = (FakeState()
          .set(gm.PLANNER, "v1").set(gm.PLAN_REVIEWER, "## Decision: REVISE")
          .set(gm.PLANNER, "v2").set(gm.PLAN_REVIEWER, "## Decision: REVISE"))
    check("replan budget spent", gm.cond_replan(s2, invocation_state=INV), False)
    check("plan_done proceeds anyway", gm.cond_plan_done(s2, invocation_state=INV), True)

    # Plan APPROVE: proceed, no replan.
    s3 = FakeState().set(gm.PLANNER, "v1").set(gm.PLAN_REVIEWER, "## Decision: APPROVE")
    check("no replan on approve", gm.cond_replan(s3, invocation_state=INV), False)
    check("plan_done on approve", gm.cond_plan_done(s3, invocation_state=INV), True)

    # Exactly one of {replan, plan_done} fires after plan_reviewer completes.
    for st in (s, s2, s3):
        fired = int(gm.cond_replan(st, invocation_state=INV)) + int(gm.cond_plan_done(st, invocation_state=INV))
        check("exactly-one plan exit", fired, 1)

    # Impl loop: 1st impl + REVISE -> needs fix (1 run < 4).
    si = FakeState().set(gm.IMPLEMENTER, "impl1").set(gm.IMPL_REVIEWER, "## Decision: REVISE")
    check("needs_fix when iters remain", gm.cond_needs_fix(si, invocation_state=INV), True)

    # 4 impl runs + REVISE -> budget exhausted, no more fixes.
    si4 = FakeState()
    for i in range(4):
        si4.set(gm.IMPLEMENTER, f"impl{i+1}").set(gm.IMPL_REVIEWER, "## Decision: REVISE")
    check("no fix when maxed", gm.cond_needs_fix(si4, invocation_state=INV), False)

    # APPROVE -> never needs fix.
    sa = FakeState().set(gm.IMPLEMENTER, "impl1").set(gm.IMPL_REVIEWER, "## Decision: APPROVE")
    check("no fix on approve", gm.cond_needs_fix(sa, invocation_state=INV), False)


def test_replan_no_double_impl():
    """Regression: a re-plan must NOT trigger the implementer off the stale
    plan-review verdict."""
    print("re-plan topology guard:")
    s = (FakeState()
         .set(gm.PLANNER, "v1").set(gm.PLAN_REVIEWER, "## Decision: REVISE")
         .set(gm.PLANNER, "v2"))  # re-plan happened; new review not yet run
    check("stale verdict blocks implementer", gm.cond_plan_done(s, invocation_state=INV), False)
    s.set(gm.PLAN_REVIEWER, "## Decision: APPROVE")
    check("proceeds after current review", gm.cond_plan_done(s, invocation_state=INV), True)


def test_config_and_context():
    print("config + context:")
    cfg = Config.resolve(
        service="backup", resource="BackupVault",
        controller_dir="/tmp/backup-controller",
        codegen_dir="/tmp/code-generator",
        skills_dir=str(SKILLS),
    )
    check("service", cfg.service, "backup")
    check("test_infra name", cfg.test_infra_dir.name, "test-infra")
    check("test_infra sibling of controller", cfg.test_infra_dir.parent, cfg.controller_dir.parent)
    if SKILLS.is_dir():
        from roles import context
        ctx = context.for_config(cfg)
        p = context.planner_system_prompt(ctx)
        check("planner prompt nonempty", bool(p and "Planner" in p), True)
        rv = context.reviewer_system_prompt(ctx, mode="plan")
        check("plan-review mode injected", "plan-review" in rv, True)
    else:
        print("  [skip] ack-dev-skills not present at", SKILLS)


def test_reporting_no_verdict_vs_revise():
    """Regression: empty reviewer output must report as 'no verdict', not REVISE."""
    print("reporting no-verdict vs revise:")
    from roles.orchestrator import RunResult
    cfg = Config.resolve(service="s", resource="R", controller_dir="/tmp/c",
                         codegen_dir="/tmp/g", skills_dir=str(SKILLS))
    rr = RunResult(cfg=cfg, final_review_text="", impl_reviewer_ran=False, impl_decision=None)
    check("no-verdict not approved", rr.approved, False)
    check("no-verdict decision is None", rr.impl_decision, None)
    rr2 = RunResult(cfg=cfg, final_review_text="## Decision: APPROVE",
                    impl_reviewer_ran=True, impl_decision=Decision.APPROVE)
    check("approved", rr2.approved, True)


def test_progress_reporter():
    """ProgressReporter renders node banners + reviewer verdicts; verbose streams text."""
    print("progress reporter:")
    from roles.progress import ProgressReporter, consume
    from roles import graph as g

    class NR:  # minimal NodeResult stand-in
        class _S: name = "COMPLETED"
        status = _S()
        def __init__(self, text): self._t = text
        def get_agent_results(self): return [FakeAgentResult(self._t)] if self._t else []

    events = [
        {"type": "multiagent_node_start", "node_id": g.PLANNER, "node_type": "agent"},
        {"type": "multiagent_node_stream", "node_id": g.PLANNER, "event": {"data": "thinking"}},
        {"type": "multiagent_node_stop", "node_id": g.PLANNER, "node_result": NR("plan")},
        {"type": "multiagent_node_start", "node_id": g.IMPL_REVIEWER, "node_type": "agent"},
        {"type": "multiagent_node_stop", "node_id": g.IMPL_REVIEWER, "node_result": NR("## Decision: APPROVE")},
        {"type": "multiagent_result", "result": object()},
    ]

    buf = io.StringIO()
    consume(events, ProgressReporter(verbose=False, stream=buf))
    out = buf.getvalue()
    check("planner banner", "▶ planner started" in out, True)
    check("review verdict surfaced", "review completed [COMPLETED] → APPROVE" in out, True)
    check("no streamed body when quiet", "thinking" not in out, True)

    buf2 = io.StringIO()
    consume(events, ProgressReporter(verbose=True, stream=buf2))
    out2 = buf2.getvalue()
    check("streamed text in verbose", "thinking" in out2, True)
    check("stream attributed to node", "[planner]" in out2, True)


def test_ensure_test_config():
    """Phase 3 configures test_config.yaml for the target resource, preserving role."""
    print("ensure_test_config:")
    tmp = Path(tempfile.mkdtemp())
    try:
        ti = tmp / "test-infra"; ti.mkdir()
        controller = tmp / "svc-controller"; controller.mkdir()
        (ti / "test_config.yaml").write_text(
            "aws:\n  assumed_role_arn: arn:aws:iam::111:role/r\n"
            "tests:\n  methods:\n    - test_policy_engine\n"
            "debug:\n  enabled: false\n"
        )
        cfg = Config.resolve(service="svc", resource="BackupVault",
                             controller_dir=str(controller),
                             codegen_dir=str(tmp / "cg"), skills_dir=str(SKILLS))
        ok, _ = ensure_test_config(cfg)
        data = yaml.safe_load((ti / "test_config.yaml").read_text())
        check("ok", ok, True)
        check("methods retargeted", data["tests"]["methods"], ["test_backup_vault"])
        check("debug enabled", data["debug"]["enabled"], True)
        check("dump logs on", data["debug"]["dump_controller_logs"], True)
        check("role preserved", data["aws"]["assumed_role_arn"], "arn:aws:iam::111:role/r")
    finally:
        _sh.rmtree(tmp)


def main():
    for fn in (test_verdict, test_snake, test_e2e_classify, test_conditions,
               test_replan_no_double_impl, test_config_and_context,
               test_reporting_no_verdict_vs_revise, test_progress_reporter,
               test_ensure_test_config):
        fn()
    print("\nALL OFFLINE TESTS PASSED")


if __name__ == "__main__":
    main()
