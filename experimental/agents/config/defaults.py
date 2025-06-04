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
"""Default configuration values for ACK agents."""

# AWS SDK Go version
DEFAULT_AWS_SDK_GO_VERSION = "v1.32.6"

# Maximum number of log lines to return
MAX_LOG_LINES_TO_RETURN = 100

# CLI defaults for the agent
DEFAULT_REGION = "us-west-2"
DEFAULT_MODEL_ID = "us.anthropic.claude-3-7-sonnet-20250219-v1:0"
DEFAULT_TEMPERATURE = 0.2

# Memory Agent User ID
MEM0_USER_ID = "ack_codegen_agent_user"
