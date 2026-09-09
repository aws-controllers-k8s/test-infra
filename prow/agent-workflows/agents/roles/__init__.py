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
"""Role-based add-resource harness.

Implements the ack-dev-skills add-resource workflow as a Strands multi-agent
Graph running a Planner -> Plan-Review -> Implementer -> Review -> E2E loop.
The four role agents compose their system prompts at runtime from an
ack-dev-skills checkout (nothing vendored), so the role SOPs and schemas remain
the single source of truth for ACK domain knowledge.

This replaces the earlier task-based (Model -> Generator -> Tag) pipeline. Loop
control is deterministic and lives in Python edge conditions (see graph.py); the
LLM only decides APPROVE vs REVISE.
"""
