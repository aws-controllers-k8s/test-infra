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
"""Strands Graph topology + deterministic loop control for add-resource.

Phases 1, 1.5, and 2 of workflows/add-resource.md run as a Strands Graph:

    planner ─always────────────▶ plan_reviewer
    plan_reviewer ─REVISE&replan─▶ planner          (re-plan loop, max 1)
    plan_reviewer ─plan_done─────▶ implementer       (trigger; carries review)
    planner ──────plan_done──────▶ implementer       (carries the plan document)
    implementer ─always──────────▶ impl_reviewer
    impl_reviewer ─REVISE&iters──▶ implementer        (fix loop, max 4 total)
    impl_reviewer ─APPROVE|maxed─▶ (no edge ⇒ graph terminates)

Loop control is fully deterministic and lives in edge conditions — never in the
LLM's routing. Conditions derive iteration counts read-only from
`state.execution_order` and read their *limits* from `invocation_state` (the
configurable "counter layer"). The LLM only decides APPROVE vs REVISE; the
harness decides whether the bounds permit another pass.

Phase 3 (E2E) is shell orchestration, not an LLM graph node, so it lives in the
orchestrator wrapped around this graph (see orchestrator.py / e2e.py).
"""

from __future__ import annotations

from typing import Any

from strands.multiagent import GraphBuilder
from strands.multiagent.graph import Graph, GraphState

from .agents import AgentSet
from .config import Config
from .verdict import Decision, parse_decision

# Node ids — also the keys in GraphState.results and the labels in
# execution_order. Kept as constants so conditions and the orchestrator agree.
PLANNER = "planner"
PLAN_REVIEWER = "plan_reviewer"
IMPLEMENTER = "implementer"
IMPL_REVIEWER = "impl_reviewer"

# invocation_state keys carrying the configurable bounds.
KEY_MAX_REPLAN = "max_replan_attempts"
KEY_MAX_IMPL = "max_impl_iterations"


# --------------------------------------------------------------------------
# State inspection helpers (read-only).
# --------------------------------------------------------------------------
def _node_text(state: GraphState, node_id: str) -> str:
    """Return the latest agent text output for a node, or '' if not run yet."""
    result = state.results.get(node_id)
    if result is None:
        return ""
    agent_results = result.get_agent_results()
    if not agent_results:
        return ""
    return str(agent_results[-1])


def _run_count(state: GraphState, node_id: str) -> int:
    """How many times a node has executed (counts revisits)."""
    return sum(1 for n in state.execution_order if n.node_id == node_id)


def _plan_decision(state: GraphState) -> Decision:
    return parse_decision(_node_text(state, PLAN_REVIEWER))


def _impl_decision(state: GraphState) -> Decision:
    return parse_decision(_node_text(state, IMPL_REVIEWER))


# --------------------------------------------------------------------------
# Edge conditions (context conditions: must accept invocation_state).
# --------------------------------------------------------------------------
def _replan_attempts_used(state: GraphState) -> int:
    # First planner run is the initial plan; each additional run is a re-plan.
    return max(0, _run_count(state, PLANNER) - 1)


def cond_replan(state: GraphState, *, invocation_state: dict[str, Any], **_: Any) -> bool:
    """plan_reviewer -> planner: plan needs revision AND a re-plan is allowed."""
    if _plan_decision(state) != Decision.REVISE:
        return False
    max_replan = invocation_state.get(KEY_MAX_REPLAN, 1)
    return _replan_attempts_used(state) < max_replan


def cond_plan_done(state: GraphState, *, invocation_state: dict[str, Any], **_: Any) -> bool:
    """planner/plan_reviewer -> implementer: proceed to implementation.

    True once the plan is APPROVED, or REVISE but the re-plan budget is spent
    (the workflow proceeds with the best available plan). This is the exact
    complement of cond_replan given a completed plan_reviewer, so exactly one
    of the two edges out of plan_reviewer fires.
    """
    text = _node_text(state, PLAN_REVIEWER)
    if not text:  # plan_reviewer has not run yet
        return False
    # Guard against a stale verdict during a re-plan: right after the planner
    # re-runs, plan_reviewer still holds the PREVIOUS round's result and the
    # re-plan budget may already read as spent, which would fire the
    # planner->implementer edge before the new plan is reviewed. Require the
    # plan-review to be "current" — run at least as many times as the planner —
    # so the implementer only proceeds once THIS round's review exists.
    if _run_count(state, PLAN_REVIEWER) < _run_count(state, PLANNER):
        return False
    if _plan_decision(state) == Decision.APPROVE:
        return True
    # REVISE: proceed only when no further re-plan is permitted.
    return not cond_replan(state, invocation_state=invocation_state)


def cond_needs_fix(state: GraphState, *, invocation_state: dict[str, Any], **_: Any) -> bool:
    """impl_reviewer -> implementer: revise AND iterations remain.

    iteration budget = 1 initial implementation + (max_impl - 1) review-driven
    passes, i.e. the implementer may run at most max_impl times total.
    """
    if _impl_decision(state) != Decision.REVISE:
        return False
    max_impl = invocation_state.get(KEY_MAX_IMPL, 4)
    return _run_count(state, IMPLEMENTER) < max_impl


# --------------------------------------------------------------------------
# Graph construction.
# --------------------------------------------------------------------------
def build_graph(cfg: Config, agents: AgentSet) -> Graph:
    b = GraphBuilder()
    b.add_node(agents.planner, PLANNER)
    b.add_node(agents.plan_reviewer, PLAN_REVIEWER)
    b.add_node(agents.implementer, IMPLEMENTER)
    b.add_node(agents.impl_reviewer, IMPL_REVIEWER)

    # Phase 1 -> 1.5
    b.add_edge(PLANNER, PLAN_REVIEWER)
    # Phase 1.5 re-plan loop
    b.add_edge(PLAN_REVIEWER, PLANNER, condition=cond_replan)
    # Phase 1.5 -> 2: implementer needs BOTH the plan (from planner) and the
    # plan-review (from plan_reviewer). The plan_reviewer edge is the trigger;
    # the planner edge carries the plan document into the assembled input.
    b.add_edge(PLAN_REVIEWER, IMPLEMENTER, condition=cond_plan_done)
    b.add_edge(PLANNER, IMPLEMENTER, condition=cond_plan_done)
    # Phase 2 implementation/review loop
    b.add_edge(IMPLEMENTER, IMPL_REVIEWER)
    b.add_edge(IMPL_REVIEWER, IMPLEMENTER, condition=cond_needs_fix)

    b.set_entry_point(PLANNER)
    # Clean slate on each revisit so re-runs rebuild input purely from the
    # current handoff documents, mirroring stateless subagent dispatch.
    b.reset_on_revisit(True)

    # Safety net well above the deterministic bounds enforced by conditions.
    expected = 1 + 2 * (cfg.max_replan_attempts + 1) + 2 * cfg.max_impl_iterations
    b.set_max_node_executions(expected + 4)
    b.set_execution_timeout(cfg.extra.get("graph_timeout_s", 3600))
    b.set_node_timeout(cfg.extra.get("node_timeout_s", 1800))
    b.set_graph_id("ack-add-resource")

    return b.build()


def invocation_state_for(cfg: Config) -> dict[str, Any]:
    """Runtime context forwarded to edge conditions (the counter layer)."""
    return {
        KEY_MAX_REPLAN: cfg.max_replan_attempts,
        KEY_MAX_IMPL: cfg.max_impl_iterations,
    }
