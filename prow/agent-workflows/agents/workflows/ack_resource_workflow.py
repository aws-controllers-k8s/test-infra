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

from dataclasses import dataclass
from typing import Optional

from config.defaults import DEFAULT_MODEL_ID
from roles.workflow import ADD_RESOURCE
from workflows.ack_workflow import (
    ACKWorkflowRunner,
    WorkflowRequest,
)


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
    """Public add-resource adapter backed by the shared workflow runner."""

    def __init__(self, runner: ACKWorkflowRunner | None = None) -> None:
        self._runner = runner or ACKWorkflowRunner()

    async def run(self, input_data: ResourceAdditionInput) -> ResourceAdditionOutput:
        result = await self._runner.run(
            WorkflowRequest(
                definition=ADD_RESOURCE,
                service=input_data.service,
                resource=input_data.resource,
                aws_sdk_version=input_data.aws_sdk_version,
                model_id=input_data.model_id,
            )
        )
        return ResourceAdditionOutput(
            success=result.success,
            service=result.service,
            resource=result.resource,
            build_logs=result.build_logs,
            error_message=result.error_message,
            config_changes=result.config_changes,
            report=result.report,
        )


def create_ack_resource_workflow() -> ACKResourceWorkflow:
    """Factory for the add-resource workflow (stable public entry point)."""
    return ACKResourceWorkflow()
