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
"""Compose system prompts from ack-dev-skills content read at runtime.

This mirrors how the Claude Code subagent definitions in
`ack-dev-skills/agents/*.md` inline the role SOPs and schemas via `@`-includes.
Here we read the same source files and assemble equivalent system prompts for
the Strands agents, so the two harnesses share a single source of truth.
"""

from __future__ import annotations

from dataclasses import dataclass
from pathlib import Path

from .config import Config


def _read(path: Path) -> str:
    if not path.is_file():
        raise FileNotFoundError(f"required ack-dev-skills file missing: {path}")
    return path.read_text(encoding="utf-8")


@dataclass
class SkillsContext:
    """Lazily-read views over an ack-dev-skills checkout."""

    root: Path

    # --- raw file readers -------------------------------------------------
    def skill_md(self) -> str:
        return _read(self.root / "skills" / "ack-dev" / "SKILL.md")

    def role(self, name: str) -> str:
        """name in {planner, implementer, reviewer}."""
        return _read(self.root / "roles" / f"{name}.md")

    def schema(self, name: str) -> str:
        """name in {plan-output, review-output}."""
        return _read(self.root / "roles" / "schemas" / f"{name}.md")

    def workflow(self) -> str:
        return _read(self.root / "workflows" / "add-resource.md")

    def reference(self, name: str) -> str:
        """Shared reference doc, e.g. 'generator-yaml-reference'."""
        return _read(self.root / "references" / f"{name}.md")


# Behavioral guardrails copied from the agents/*.md wrappers. Kept here (not in
# ack-dev-skills) because they are harness-level framing, not domain knowledge.
_PLANNER_FRAME = """You are the ACK Resource Planner. Your sole job is to research \
an AWS resource and produce a structured implementation plan. Follow the SOP \
methodology exactly. Produce the plan document (matching the Plan Output Schema) \
as your final output.

You must NOT:
- Write any code
- Modify any files
- Create or edit generator.yaml
- Make implementation decisions that aren't supported by your research

CONTEXT BUDGET — read SDK files surgically. The aws-sdk-go-v2 service module
contains very large generated files (deserializers.go, serializers.go,
validators.go are often 200KB-850KB each). NEVER read those whole — they will
overflow the context window. To inventory a resource's API:
- Read only the per-operation files: api_op_Create<R>.go, api_op_Get<R>.go,
  api_op_Update<R>.go, api_op_Delete<R>.go, api_op_List<R>s.go
- Read the shared shapes from types/types.go, but grep for the specific struct
  names first and read only the relevant ranges rather than the entire file

SHELL DISCIPLINE — locate files deterministically; never scan broad roots.
Every shell command is killed after a timeout, and a filesystem-wide scan will
hit it and waste the whole budget. Specifically:
- To find the SDK service module directory, use the Go toolchain, NOT find:
    go list -m -f '{{.Dir}}' github.com/aws/aws-sdk-go-v2/service/<service> 2>/dev/null
  (run from CONTROLLER_DIR; this prints the exact cached module path instantly).
  Then ls / grep WITHIN that directory only.
- NEVER run `find /`, `find ~`, `grep -r` / `grep -rn` over `/`, `$HOME`,
  `$(go env GOMODCACHE)/..`, or any directory above the specific module/repo you
  are inspecting. Scope every grep/find to a known directory (the controller
  repo, the code-generator repo, or the resolved SDK module dir).
- Prefer ripgrep (`rg`) with an explicit path argument over recursive grep."""

_IMPLEMENTER_FRAME = """You are the ACK Resource Implementer. You take a structured \
plan or reviewer feedback and produce working code following ACK conventions. \
Follow the SOP methodology exactly. Your output is working code that builds \
cleanly, plus a summary of changes for the Reviewer.

You must NOT:
- Research AWS APIs (trust the plan)
- Edit generated files (apis/, pkg/resource/, config/crd/, config/rbac/, helm/, cmd/)
- Add configuration for fields that use all defaults
- Deviate from the plan without documenting the reason

You may ONLY edit:
- generator.yaml
- templates/hooks/
- test/e2e/
- sdk/resource/<resource-name>/hooks.go
- sdk/resource/<resource-name>/custom_*.go"""

_REVIEWER_FRAME = """You are the ACK Resource Reviewer. You inspect the \
Implementer's work (or, in plan-review mode, the plan) against the plan and ACK \
conventions. Follow the SOP checklist exactly. Your output is a review document \
matching the Review Output Schema, beginning with a line `## Decision: APPROVE` \
or `## Decision: REVISE`.

You must NOT:
- Modify any code files
- Run code generation
- Make changes to fix issues yourself
- Approve work that doesn't compile
- Approve missing field renames (these always cause bugs)

SHELL DISCIPLINE — when verifying claims against the SDK, locate files
deterministically and never scan broad roots. Every shell command is killed
after a timeout, so a filesystem-wide scan wastes the budget. To find the SDK
service module, use `go list -m -f '{{.Dir}}' github.com/aws/aws-sdk-go-v2/service/<service>`
(run from CONTROLLER_DIR), then grep WITHIN that directory. NEVER run `find /`,
`find ~`, or `grep -r` over `/`, `$HOME`, or `$(go env GOMODCACHE)/..`. Scope
every grep/find to a known directory."""


def _compose(frame: str, sections: dict[str, str]) -> str:
    """Assemble a system prompt: harness frame, then labeled doc sections."""
    parts = [frame]
    for title, body in sections.items():
        parts.append(f"\n\n===== {title} =====\n\n{body.strip()}")
    return "".join(parts)


def planner_system_prompt(ctx: SkillsContext) -> str:
    return _compose(
        _PLANNER_FRAME,
        {
            "ACK DEVELOPMENT GUIDE (ack-dev SKILL)": ctx.skill_md(),
            "ROLE SOP: PLANNER": ctx.role("planner"),
            "OUTPUT SCHEMA: PLAN": ctx.schema("plan-output"),
        },
    )


def implementer_system_prompt(ctx: SkillsContext) -> str:
    return _compose(
        _IMPLEMENTER_FRAME,
        {
            "ACK DEVELOPMENT GUIDE (ack-dev SKILL)": ctx.skill_md(),
            "ROLE SOP: IMPLEMENTER": ctx.role("implementer"),
            # The implementer reads plans and consumes review feedback, so give
            # it both schemas for reference.
            "INPUT SCHEMA: PLAN": ctx.schema("plan-output"),
            "INPUT SCHEMA: REVIEW FEEDBACK": ctx.schema("review-output"),
        },
    )


_PLAN_REVIEW_MODE = """\

OPERATING MODE FOR THIS NODE: plan-review.
You are reviewing the PLAN DOCUMENT (not implementation code). Execute the Plan
Review Checklist from your SOP and skip the implementation-review methodology
sections. Verify API constraints and custom-code necessity against the SDK model."""

_IMPL_REVIEW_MODE = """\

OPERATING MODE FOR THIS NODE: implementation-review (default).
You are reviewing the Implementer's output against the plan. Execute the full
implementation-review methodology and checklist from your SOP."""


def reviewer_system_prompt(ctx: SkillsContext, *, mode: str = "impl") -> str:
    """mode in {'plan', 'impl'} — selects plan-review vs implementation-review."""
    frame = _REVIEWER_FRAME + (_PLAN_REVIEW_MODE if mode == "plan" else _IMPL_REVIEW_MODE)
    return _compose(
        frame,
        {
            "ACK DEVELOPMENT GUIDE (ack-dev SKILL)": ctx.skill_md(),
            "ROLE SOP: REVIEWER": ctx.role("reviewer"),
            "OUTPUT SCHEMA: REVIEW": ctx.schema("review-output"),
            "REFERENCE: PLAN SCHEMA (what the plan should contain)": ctx.schema(
                "plan-output"
            ),
        },
    )


def for_config(cfg: Config) -> SkillsContext:
    return SkillsContext(root=cfg.skills_dir)
