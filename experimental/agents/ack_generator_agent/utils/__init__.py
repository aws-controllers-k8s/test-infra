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
"""Utilities for ACK Generator tools."""

from ack_generator_agent.utils.settings import settings
from ack_generator_agent.utils.repo import (
    ensure_ack_directories,
    ensure_code_generator_cloned,
    ensure_runtime_cloned,
    ensure_aws_sdk_go_v2_cloned,
    check_service_controller_exists,
    ensure_service_repo_cloned,
    get_release_version
)
from ack_generator_agent.utils.constants import (
    DEFAULT_AWS_SDK_GO_VERSION,
    MAX_LOG_LINES_TO_RETURN,
    ACK_SYSTEM_PROMPT,
    MEMORY_AGENT_SYSTEM_PROMPT,
    DOCS_AGENT_SYSTEM_PROMPT
)
from ack_generator_agent.utils.docs_agent import DocsAgent