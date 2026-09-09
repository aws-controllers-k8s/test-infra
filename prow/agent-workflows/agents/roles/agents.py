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
"""Construct the four role agents (planner, implementer, two reviewer modes).

Each agent is a Strands `Agent` backed by a Bedrock Claude model built through
the shared `utils.bedrock` factory (enhanced retries/timeouts), with a system
prompt composed from ack-dev-skills content (see context.py) and a tool set that
mirrors the `tools:` frontmatter of the corresponding ack-dev-skills/agents/*.md
subagent definition:

    ack-planner     -> Read, Grep, Glob, Bash, WebFetch
    ack-implementer -> Read, Write, Edit, Grep, Glob, Bash
    ack-reviewer    -> Read, Grep, Glob, Bash

Strands tool equivalents:
    Read/Grep/Glob  -> file_read (strands_tools; read-only)
    Write/Edit      -> file_editor (strands.vended_tools; read + write + edit)
    Bash            -> shell (strands.vended_tools)
    WebFetch        -> http_request (strands.vended_tools)

We use the vended tools from strands-agents core (`strands.vended_tools`) rather
than the deprecated `strands_tools` shell/editor. The vended shell/file_editor
route through the agent's sandbox; since we never pass `sandbox=` to Agent, that
resolves to `NotASandboxLocalEnvironment`, which runs commands and file ops
directly on the host (no isolation) — the same behavior the old tools had, which
is what the on-host `make build-controller`/`git` steps require.

`file_read` is kept for the read-only planner/reviewer roles: it enforces the
"do not modify files" boundary at the tool level and has no vended read-only
equivalent (file_editor can write). Tools are imported lazily inside
build_agents so this module (and its importers) load even where the tool
packages are absent — e.g. the offline logic tests, which never build agents.
"""

from __future__ import annotations

from dataclasses import dataclass

from strands import Agent
from strands.agent.conversation_manager import SlidingWindowConversationManager

from utils.bedrock import create_enhanced_bedrock_model

from . import context
from .config import Config


def _conversation_manager() -> SlidingWindowConversationManager:
    """Keep context bounded across a long agent loop.

    `should_truncate_results=True` truncates an oversized tool result (e.g. an
    accidental read of a 400K+ generated SDK file) instead of letting it
    overflow the model context window and fail the whole run. Proactive
    compression trims older turns before the window is hit.
    """
    return SlidingWindowConversationManager(
        window_size=40,
        should_truncate_results=True,
    )


@dataclass
class AgentSet:
    planner: Agent
    implementer: Agent
    # The reviewer SOP has two modes; the graph uses them as two distinct nodes
    # because a Strands graph node maps to exactly one executor.
    plan_reviewer: Agent
    impl_reviewer: Agent
    # Lightweight summarizer that composes the PR description from text already
    # gathered (plan/impl/review/e2e). No tools. Optional so callers that build
    # an AgentSet directly (e.g. tests) need not supply it.
    pr_writer: Agent | None = None


def _role_agent(cfg: Config, *, name: str, model_id: str, system_prompt: str, tools: list) -> Agent:
    """Build one role agent through the shared enhanced Bedrock factory.

    The default PrintingCallbackHandler is suppressed (callback_handler=None):
    the orchestrator renders all node output via stream_async + ProgressReporter,
    so the per-agent printer would only duplicate (and de-attribute) that text.
    """
    model = create_enhanced_bedrock_model(
        model_id=model_id,
        region_name=cfg.region,
        temperature=cfg.temperature if cfg.temperature is not None else 0.2,
        max_tokens=cfg.max_tokens,
    )
    return Agent(
        name=name,
        model=model,
        system_prompt=system_prompt,
        tools=tools,
        conversation_manager=_conversation_manager(),
        callback_handler=None,
    )


def build_agents(cfg: Config) -> AgentSet:
    """Build the planner/implementer/reviewer agents for one run."""
    # Imported here (not at module top) so importing this module does not require
    # the tool packages to be installed — see the module docstring.
    from strands.vended_tools import file_editor, http_request, shell
    from strands_tools import file_read  # read-only; no vended equivalent yet

    ctx = context.for_config(cfg)

    planner = _role_agent(
        cfg,
        name="ack-planner",
        model_id=cfg.planner_model,
        system_prompt=context.planner_system_prompt(ctx),
        # Planner researches but does not write: read, search, shell, web.
        tools=[file_read, shell, http_request],
    )

    implementer = _role_agent(
        cfg,
        name="ack-implementer",
        model_id=cfg.implementer_model,
        system_prompt=context.implementer_system_prompt(ctx),
        # Implementer is the only writer. file_editor reads + writes + edits.
        tools=[file_editor, shell],
    )

    plan_reviewer = _role_agent(
        cfg,
        name="ack-plan-reviewer",
        model_id=cfg.reviewer_model,
        system_prompt=context.reviewer_system_prompt(ctx, mode="plan"),
        # Reviewer reads and runs builds/tests but never writes.
        tools=[file_read, shell],
    )

    impl_reviewer = _role_agent(
        cfg,
        name="ack-impl-reviewer",
        model_id=cfg.reviewer_model,
        system_prompt=context.reviewer_system_prompt(ctx, mode="impl"),
        tools=[file_read, shell],
    )

    # No tools: it summarizes text passed in the prompt, never touches the repo.
    pr_writer = _role_agent(
        cfg,
        name="ack-pr-writer",
        model_id=cfg.reviewer_model,
        system_prompt=(
            "You write concise, reader-facing GitHub pull request descriptions for "
            "ACK service-controller changes that add a new resource. You are given "
            "the implementation plan, the implementer's change summary, the "
            "reviewer's verdict, and the e2e result. Output GitHub-flavored markdown "
            "only, with no preamble. Structure it as: a 2-3 sentence summary of what "
            "was added; a '## Key design decisions' bulleted list drawn from the plan "
            "(CRUD operation mapping, immutable and ignored/deprecated fields, tags "
            "handling, custom hooks, terminal error codes) — include only decisions "
            "that actually apply; and a short '## Testing' line with the e2e result. "
            "Be factual and terse; never invent details not present in the inputs."
        ),
        tools=[],
    )

    return AgentSet(
        planner=planner,
        implementer=implementer,
        plan_reviewer=plan_reviewer,
        impl_reviewer=impl_reviewer,
        pr_writer=pr_writer,
    )
