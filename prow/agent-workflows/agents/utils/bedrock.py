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
"""Utility functions for enhanced Bedrock model configuration."""

from typing import Optional
from botocore.config import Config as BotocoreConfig
from strands.models import BedrockModel
from strands import Agent

from config.defaults import (
    DEFAULT_MODEL_ID,
    DEFAULT_REGION,
    DEFAULT_TEMPERATURE,
    DEFAULT_MAX_RETRY_ATTEMPTS,
    DEFAULT_RETRY_MODE,
    DEFAULT_CONNECT_TIMEOUT,
    DEFAULT_READ_TIMEOUT
)


def create_enhanced_boto_config() -> BotocoreConfig:
    """
    Create a BotocoreConfig with enhanced timeout and retry settings.
    
    Returns:
        BotocoreConfig: Configured boto client config with enhanced reliability settings
    """
    return BotocoreConfig(
        retries={
            "max_attempts": DEFAULT_MAX_RETRY_ATTEMPTS,
            "mode": DEFAULT_RETRY_MODE
        },
        connect_timeout=DEFAULT_CONNECT_TIMEOUT,
        read_timeout=DEFAULT_READ_TIMEOUT,
    )


def create_enhanced_bedrock_model(
    model_id: str = DEFAULT_MODEL_ID,
    region_name: str = DEFAULT_REGION,
    temperature: Optional[float] = DEFAULT_TEMPERATURE,
    max_tokens: Optional[int] = None,
    **kwargs,
) -> BedrockModel:
    """
    Create a BedrockModel with enhanced reliability settings.
    
    Args:
        model_id: The Bedrock model ID to use
        region_name: AWS region for Bedrock
        temperature: Sampling temperature (optional; omitted by default)
        max_tokens: Maximum tokens to generate (optional)
        **kwargs: Additional BedrockModel configuration parameters
        
    Returns:
        BedrockModel: Configured BedrockModel with enhanced reliability settings
    """
    boto_config = create_enhanced_boto_config()
    
    model_config = {
        "model_id": model_id,
        "region_name": region_name,
        "boto_client_config": boto_config,
        **kwargs,
    }

    if temperature is not None:
        model_config["temperature"] = temperature

    if max_tokens is not None:
        model_config["max_tokens"] = max_tokens
    
    return BedrockModel(**model_config)


def create_enhanced_agent(
    tools: list,
    system_prompt: str,
    model_id: str = DEFAULT_MODEL_ID,
    region_name: str = DEFAULT_REGION,
    temperature: Optional[float] = DEFAULT_TEMPERATURE,
    max_tokens: Optional[int] = None,
    **model_kwargs,
) -> Agent:
    """
    Create an Agent with an enhanced BedrockModel configuration.
    
    Args:
        tools: List of tools for the agent
        system_prompt: System prompt for the agent
        model_id: The Bedrock model ID to use
        region_name: AWS region for Bedrock
        temperature: Sampling temperature (optional; omitted by default)
        max_tokens: Maximum tokens to generate (optional)
        **model_kwargs: Additional BedrockModel configuration parameters
        
    Returns:
        Agent: Configured Agent with enhanced BedrockModel
    """
    bedrock_model = create_enhanced_bedrock_model(
        model_id=model_id,
        region_name=region_name,
        temperature=temperature,
        max_tokens=max_tokens,
        **model_kwargs,
    )
    
    return Agent(
        model=bedrock_model,
        tools=tools,
        system_prompt=system_prompt,
    ) 

def converse_text(
    system: str,
    user: str,
    *,
    model_id: str = DEFAULT_MODEL_ID,
    region_name: str = DEFAULT_REGION,
    max_tokens: int = 1024,
    client=None,
) -> tuple[str, dict]:
    """One Bedrock Converse request with no tools.

    No toolConfig is sent, so the model has nothing to call and can only
    answer with text: ACK Autopilot's triage workflow (Plan 8) relies on this
    for the untrusted-input firewall (design 5.2). Returns the reply text and
    the token counts as {"input", "output", "total"}. A reply that stops for
    tool use is an error.
    """
    if client is None:
        import boto3

        client = boto3.client("bedrock-runtime", region_name=region_name, config=create_enhanced_boto_config())
    resp = client.converse(
        modelId=model_id,
        system=[{"text": system}],
        messages=[{"role": "user", "content": [{"text": user}]}],
        inferenceConfig={"maxTokens": max_tokens},
    )
    if resp.get("stopReason") == "tool_use":
        raise RuntimeError("the model asked for a tool, and none is offered")
    content = ((resp.get("output") or {}).get("message") or {}).get("content") or []
    text = "".join(p["text"] for p in content if isinstance(p, dict) and isinstance(p.get("text"), str))
    usage = resp.get("usage") or {}

    def count(key: str) -> int:
        value = usage.get(key)
        return value if isinstance(value, int) and not isinstance(value, bool) and value >= 0 else 0

    return text, {"input": count("inputTokens"), "output": count("outputTokens"), "total": count("totalTokens")}
