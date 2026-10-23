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
"""Address-feedback workflow adapter built on the shared role-based ACK harness.

The job script checks out the pull request head and gathers the review
feedback before this runs. The pull request URL, its base commit, and the
feedback file path arrive through the environment (see ADDRESS_FEEDBACK's
context_env).
"""

from __future__ import annotations

from dataclasses import dataclass
from typing import Optional

from config.defaults import DEFAULT_MODEL_ID
from roles.workflow import ADDRESS_FEEDBACK
from workflows.ack_workflow import ACKWorkflowRunner, WorkflowRequest


@dataclass
class FeedbackInput:
    """Inputs for addressing review feedback on one agent pull request."""

    service: str
    resource: str
    # Set when the pull request came from the add-field workflow.
    field: Optional[str] = None
    aws_sdk_version: Optional[str] = None
    model_id: str = DEFAULT_MODEL_ID


@dataclass
class FeedbackOutput:
    """Result of one address-feedback run."""

    success: bool
    service: str
    resource: str
    field: Optional[str] = None
    build_logs: str = ""
    error_message: str = ""
    config_changes: str = ""
    report: str = ""


class ACKFeedbackWorkflow:
    """Public address-feedback adapter backed by the shared workflow runner."""

    def __init__(self, runner: ACKWorkflowRunner | None = None) -> None:
        self._runner = runner or ACKWorkflowRunner()

    async def run(self, input_data: FeedbackInput) -> FeedbackOutput:
        result = await self._runner.run(
            WorkflowRequest(
                definition=ADDRESS_FEEDBACK,
                service=input_data.service,
                resource=input_data.resource,
                field=input_data.field,
                aws_sdk_version=input_data.aws_sdk_version,
                model_id=input_data.model_id,
            )
        )
        return FeedbackOutput(
            success=result.success,
            service=result.service,
            resource=result.resource,
            field=input_data.field,
            build_logs=result.build_logs,
            error_message=result.error_message,
            config_changes=result.config_changes,
            report=result.report,
        )


def create_ack_feedback_workflow() -> ACKFeedbackWorkflow:
    """Factory for the address-feedback workflow."""
    return ACKFeedbackWorkflow()
