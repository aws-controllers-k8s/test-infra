# Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
#
# Licensed under the Apache License, Version 2.0 (the "License"). You may not use this file except in compliance
# with the License. A copy of the License is located at
#
#    http://www.apache.org/licenses/LICENSE-2.0
#
# or in the 'license' file accompanying this file. This file is distributed on an 'AS IS' BASIS, WITHOUT WARRANTIES
# OR CONDITIONS OF ANY KIND, either express or implied. See the License for the specific language governing permissions
# and limitations under the License.
"""Shared execution adapter for role-based ACK workflows."""

from __future__ import annotations

import asyncio
import logging
import os
from dataclasses import dataclass
from pathlib import Path

from roles import orchestrator
from roles.config import Config
from roles.workflow import WorkflowDefinition
from workflows.validation import validate_inputs

logger = logging.getLogger(__name__)


@dataclass
class WorkflowRequest:
    """Normalized request from a public workflow adapter."""

    definition: WorkflowDefinition
    service: str
    resource: str
    field: str | None = None
    aws_sdk_version: str | None = None
    model_id: str | None = None


@dataclass
class WorkflowRunOutput:
    """Normalized result mapped into each public adapter's output type."""

    success: bool
    service: str
    resource: str
    field: str | None = None
    build_logs: str = ""
    error_message: str = ""
    config_changes: str = ""
    report: str = ""


class ACKWorkflowRunner:
    """Run the common Planner, Reviewer, Implementer, Reviewer, E2E graph."""

    async def run(self, request: WorkflowRequest) -> WorkflowRunOutput:
        input_problems = validate_inputs(
            service=request.service,
            resource=request.resource,
            field=request.field,
            require_field=request.definition.requires_field,
        )
        if input_problems:
            return self._error_output(
                request,
                "invalid workflow input: " + "; ".join(input_problems),
            )

        cfg = self._build_config(request)
        path_problems = cfg.validate_paths()
        if path_problems:
            message = "; ".join(path_problems)
            logger.error("configuration problems: %s", message)
            return self._error_output(
                request,
                f"cannot run {cfg.workflow_name}: required checkouts are missing: "
                f"{message}. ack-dev-skills is delivered as a Prow extra_ref and "
                "code-generator is cloned at startup; verify both are present.",
            )

        logger.info(
            "starting role-based %s: service=%s resource=%s field=%s controller=%s "
            "codegen=%s skills=%s model=%s e2e=%s",
            cfg.workflow_name,
            cfg.service,
            cfg.resource,
            cfg.field,
            cfg.controller_dir,
            cfg.codegen_dir,
            cfg.skills_dir,
            cfg.model_id,
            cfg.run_e2e,
        )

        # The orchestrator owns a synchronous event loop and blocking E2E
        # subprocess. Keep both off this adapter's already-running event loop.
        run_result = await asyncio.to_thread(
            orchestrator.run,
            cfg,
            verbose=True,
            progress=True,
        )

        report = orchestrator.completion_report(run_result)
        print("\n" + "=" * 72)
        print(report)

        # prow-job.sh reads this body when opening a PR. Failure to write it is
        # non-fatal because the wrapper has a deterministic fallback body.
        if run_result.pr_body:
            pr_body_file = os.environ.get("PR_BODY_FILE")
            if pr_body_file:
                try:
                    Path(pr_body_file).write_text(run_result.pr_body)
                except OSError as exc:
                    logger.warning("could not write PR body to %s: %s", pr_body_file, exc)

        e2e_passed = run_result.e2e is not None and run_result.e2e.status == "PASS"
        success = run_result.approved and (not cfg.run_e2e or e2e_passed)

        problems: list[str] = []
        if not run_result.approved:
            decision = run_result.impl_decision.value if run_result.impl_decision else "none"
            problems.append(f"Reviewer did not APPROVE the implementation (decision: {decision}).")
        if cfg.run_e2e and not e2e_passed:
            status = run_result.e2e.status if run_result.e2e else "not run"
            problems.append(
                f"E2E validation did not pass (status: {status}); the workflow "
                "is not done until its e2e test passes."
            )

        error_message = ""
        if problems:
            error_message = " ".join(problems) + " See the report for details."

        return WorkflowRunOutput(
            success=success,
            service=cfg.service,
            resource=cfg.resource,
            field=cfg.field,
            build_logs=run_result.impl_summary_text,
            config_changes=run_result.plan_text,
            error_message=error_message,
            report=report,
        )

    @staticmethod
    def _error_output(
        request: WorkflowRequest,
        message: str,
    ) -> WorkflowRunOutput:
        return WorkflowRunOutput(
            success=False,
            service=request.service,
            resource=request.resource,
            field=request.field,
            error_message=message,
        )

    @staticmethod
    def _build_config(request: WorkflowRequest) -> Config:
        """Resolve checkouts provided by prow-job.sh and Prow extra_refs."""
        return Config.resolve(
            service=request.service,
            resource=request.resource,
            field=request.field,
            workflow=request.definition,
            model_id=request.model_id,
            aws_sdk_go_version=request.aws_sdk_version,
            run_e2e=os.environ.get("RUN_E2E", "").lower() == "true",
        )
