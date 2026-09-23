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
"""Add-field workflow adapter built on the shared role-based ACK harness."""

from __future__ import annotations

from dataclasses import dataclass
from typing import Optional

from config.defaults import DEFAULT_MODEL_ID
from workflows.ack_resource_workflow import (
    ACKResourceWorkflow,
    ResourceAdditionInput,
)


@dataclass
class FieldAdditionInput:
    """Inputs for adding one field to an existing ACK resource."""

    service: str
    resource: str
    field: str
    aws_sdk_version: Optional[str] = None
    timeout_minutes: int = 30
    model_id: str = DEFAULT_MODEL_ID


@dataclass
class FieldAdditionOutput:
    """Result of one add-field run."""

    success: bool
    service: str
    resource: str
    field: str
    build_logs: str = ""
    error_message: str = ""
    config_changes: str = ""
    report: str = ""


class ACKFieldWorkflow:
    """Runs field-specific prompts through the shared ACK role workflow."""

    def __init__(self) -> None:
        self._workflow = ACKResourceWorkflow()

    async def run(self, input_data: FieldAdditionInput) -> FieldAdditionOutput:
        result = await self._workflow.run(
            ResourceAdditionInput(
                service=input_data.service,
                resource=input_data.resource,
                field=input_data.field,
                aws_sdk_version=input_data.aws_sdk_version,
                timeout_minutes=input_data.timeout_minutes,
                model_id=input_data.model_id,
            )
        )
        return FieldAdditionOutput(
            success=result.success,
            service=result.service,
            resource=result.resource,
            field=input_data.field,
            build_logs=result.build_logs,
            error_message=result.error_message,
            config_changes=result.config_changes,
            report=result.report,
        )


def create_ack_field_workflow() -> ACKFieldWorkflow:
    """Factory for the add-field workflow."""
    return ACKFieldWorkflow()
