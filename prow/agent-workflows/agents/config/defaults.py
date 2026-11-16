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

# CLI defaults for the agent
DEFAULT_REGION = "us-west-2"
DEFAULT_MODEL_ID = "us.anthropic.claude-opus-5-5"
# Sampling parameters are omitted by default because newer Claude models reject
# non-default temperature values. Set AGENT_TEMPERATURE only for a model that
# explicitly supports it.
DEFAULT_TEMPERATURE: float | None = None

# Boto client configuration defaults for better reliability
DEFAULT_MAX_RETRY_ATTEMPTS = 20
DEFAULT_RETRY_MODE = "standard"
DEFAULT_CONNECT_TIMEOUT = 100
DEFAULT_READ_TIMEOUT = 600
