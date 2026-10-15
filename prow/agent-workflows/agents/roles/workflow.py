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
"""Data describing the supported ACK workflow variants.

The graph, Implementer, Reviewer, E2E loop, and result handling are shared. A
workflow definition contains only the differences needed to describe a target,
select its Planner and plan schema, and add task-specific guidance.
"""

from __future__ import annotations

from dataclasses import dataclass


@dataclass(frozen=True)
class WorkflowDefinition:
    """Immutable description of one workflow variant."""

    name: str
    planner_name: str
    planner_role: str
    plan_schema: str
    task_template: str
    requires_field: bool = False
    role_instruction: str = ""
    references: tuple[str, ...] = ()

    def task_intro(self, *, service: str, resource: str, field: str | None) -> str:
        return self.task_template.format(
            service=service,
            resource=resource,
            field=field or "",
        )

    @property
    def report_title(self) -> str:
        return self.name.title()

    @staticmethod
    def report_target(*, service: str, resource: str, field: str | None) -> str:
        parts = [service, resource]
        if field:
            parts.append(field)
        return "/".join(parts)

    def context_lines(
        self,
        *,
        service: str,
        resource: str,
        field: str | None,
        controller_dir: str,
        codegen_dir: str,
        aws_sdk_go_version: str,
    ) -> str:
        values = [
            ("SERVICE", service),
            ("RESOURCE", resource),
        ]
        if self.requires_field:
            values.append(("FIELD", field or ""))
        values.extend(
            (
                ("CONTROLLER_DIR", controller_dir),
                ("CODEGEN_DIR", codegen_dir),
                ("AWS_SDK_GO_VERSION", aws_sdk_go_version),
            )
        )
        return "\n".join(f"{name}={value}" for name, value in values)


ADD_RESOURCE = WorkflowDefinition(
    name="add-resource",
    planner_name="ack-planner",
    planner_role="planner",
    plan_schema="plan-output",
    task_template="Add the {resource} resource to the {service} ACK service controller.",
)


ADD_FIELD = WorkflowDefinition(
    name="add-field",
    planner_name="ack-field-planner",
    planner_role="field-planner",
    plan_schema="field-plan-output",
    requires_field=True,
    task_template=(
        "Add the {field} field to the existing {resource} resource in the "
        "{service} ACK service controller. Do not re-plan or restructure the "
        "whole resource."
    ),
    role_instruction=(
        "OPERATING MODE: single-field addition. Follow the field-addition "
        "reference and keep all work scoped to the requested field on the "
        "existing resource."
    ),
    references=("field-addition",),
)
