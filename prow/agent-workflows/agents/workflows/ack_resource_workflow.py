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
"""Add-resource workflow adapter.

This is the stable entry point invoked by `workflows/__main__.py` (and, in the
container, by `prow-job.sh` via `python -m workflows resource-addition ...`). It
preserves the historical public API — `ResourceAdditionInput`,
`ResourceAdditionOutput`, and `create_ack_resource_workflow()` — but the body no
longer runs the old task-based (Model -> Generator -> Tag) pipeline. It now
drives the role-based Planner -> Plan-Review -> Implementer -> Review -> E2E
graph in `roles/` (see roles/orchestrator.py).

Division of labour with the shell wrapper is unchanged: `prow-job.sh` forks and
clones `<service>-controller`, mounts `ack-dev-skills` via a Prow extra_ref, and
after this workflow returns it commits the controller checkout and opens the PR.
This workflow only mutates the local controller tree via the role agents.
"""

from __future__ import annotations

import asyncio
import logging
import os
from dataclasses import dataclass, field
from pathlib import Path
from typing import Optional

from config.defaults import DEFAULT_MODEL_ID
from roles import orchestrator
from roles.config import Config

logger = logging.getLogger(__name__)


@dataclass
class ResourceAdditionInput:
    """Inputs for one add-resource run (unchanged public shape)."""

    service: str
    resource: str
    aws_sdk_version: Optional[str] = None
    timeout_minutes: int = 30
    model_id: str = DEFAULT_MODEL_ID


@dataclass
class ResourceAdditionOutput:
    """Result of one add-resource run (unchanged public shape).

    `success` reflects the Reviewer's final APPROVE verdict. `build_logs` and
    `config_changes` carry the Phase 4 completion report so the CLI can render a
    human-readable summary; they are no longer distinct artifacts.
    """

    success: bool
    service: str
    resource: str
    build_logs: str = ""
    error_message: str = ""
    config_changes: str = ""
    report: str = ""


class ACKResourceWorkflow:
    """Drives the role-based add-resource graph for a single resource."""

    async def run(self, input_data: ResourceAdditionInput) -> ResourceAdditionOutput:
        cfg = self._build_config(input_data)

        problems = cfg.validate_paths()
        if problems:
            msg = "; ".join(problems)
            logger.error("configuration problems: %s", msg)
            return ResourceAdditionOutput(
                success=False,
                service=input_data.service,
                resource=input_data.resource,
                error_message=(
                    "cannot run add-resource — required checkouts are missing: "
                    f"{msg}. ack-dev-skills is delivered as a Prow extra_ref and "
                    "code-generator is cloned at startup; verify both are present."
                ),
            )

        logger.info(
            "starting role-based add-resource: service=%s resource=%s controller=%s "
            "codegen=%s skills=%s model=%s e2e=%s",
            cfg.service, cfg.resource, cfg.controller_dir, cfg.codegen_dir,
            cfg.skills_dir, cfg.model_id, cfg.run_e2e,
        )

        # The orchestrator drives the graph via asyncio.run and then runs the
        # (blocking) E2E subprocess. Run it in a worker thread so its event loop
        # and subprocess calls never nest inside this already-running loop.
        rr = await asyncio.to_thread(
            orchestrator.run, cfg, verbose=True, progress=True
        )

        report = orchestrator.completion_report(rr)
        print("\n" + "=" * 72)
        print(report)

        # Hand the composed PR description to prow-job.sh (it reads $PR_BODY_FILE
        # for `gh pr create -F`). Only set on a successful run, which is the only
        # case a PR is opened; a write failure is non-fatal (falls back to the
        # static body in the shell wrapper).
        if rr.pr_body:
            pr_body_file = os.environ.get("PR_BODY_FILE")
            if pr_body_file:
                try:
                    Path(pr_body_file).write_text(rr.pr_body)
                except OSError as exc:
                    logger.warning("could not write PR body to %s: %s", pr_body_file, exc)

        # When E2E is enabled it is part of "done": the resource is only complete
        # if its e2e test actually PASSED. Any other status (FAIL, SKIPPED,
        # NOT_RUN, ERROR) fails the run so the harness does not open a PR for an
        # unvalidated resource. When E2E is off, success rests on the Reviewer.
        e2e_passed = rr.e2e is not None and rr.e2e.status == "PASS"
        success = rr.approved and (not cfg.run_e2e or e2e_passed)

        problems: list[str] = []
        if not rr.approved:
            problems.append(
                "Reviewer did not APPROVE the implementation "
                f"(decision: {rr.impl_decision.value if rr.impl_decision else 'none'})."
            )
        if cfg.run_e2e and not e2e_passed:
            status = rr.e2e.status if rr.e2e else "not run"
            problems.append(
                f"E2E validation did not pass (status: {status}); a resource is "
                "not done until its e2e test passes."
            )
        error_message = ""
        if problems:
            error_message = " ".join(problems) + " See the report for details."

        return ResourceAdditionOutput(
            success=success,
            service=cfg.service,
            resource=cfg.resource,
            build_logs=rr.impl_summary_text,
            config_changes=rr.plan_text,
            error_message=error_message,
            report=report,
        )

    def _build_config(self, input_data: ResourceAdditionInput) -> Config:
        """Resolve paths for the run. All checkouts are provided by the harness:

        - the controller fork by prow-job.sh (resolved from $CONTROLLER_DIR);
        - code-generator and ack-dev-skills as Prow extra_refs cloned by the
          clonerefs init container (resolved from $CODEGEN_DIR / $ACK_DEV_SKILLS_DIR,
          which the agent-plugin sets to the clonerefs paths).

        Nothing is cloned in-process; validate_paths() surfaces any missing tree.
        """
        return Config.resolve(
            service=input_data.service,
            resource=input_data.resource,
            model_id=input_data.model_id,
            aws_sdk_go_version=input_data.aws_sdk_version,
            # Phase 3 (E2E) runs only when RUN_E2E=true. The agent-plugin sets it
            # (alongside the privileged/DinD pod) for e2e-enabled workflows; off by
            # default so non-e2e runs and environments without a kind toolchain skip it.
            run_e2e=os.environ.get("RUN_E2E", "").lower() == "true",
        )


def create_ack_resource_workflow() -> ACKResourceWorkflow:
    """Factory for the add-resource workflow (stable public entry point)."""
    return ACKResourceWorkflow()
