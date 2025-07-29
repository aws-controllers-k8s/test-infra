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
    temperature: float = DEFAULT_TEMPERATURE,
    max_tokens: Optional[int] = None,
    **kwargs
) -> BedrockModel:
    """
    Create a BedrockModel with enhanced reliability settings.
    
    Args:
        model_id: The Bedrock model ID to use
        region_name: AWS region for Bedrock
        temperature: Temperature for model generation
        max_tokens: Maximum tokens to generate (optional)
        **kwargs: Additional BedrockModel configuration parameters
        
    Returns:
        BedrockModel: Configured BedrockModel with enhanced reliability settings
    """
    boto_config = create_enhanced_boto_config()
    
    model_config = {
        "model_id": model_id,
        "region_name": region_name,
        "temperature": temperature,
        "boto_client_config": boto_config,
        **kwargs
    }
    
    if max_tokens is not None:
        model_config["max_tokens"] = max_tokens
    
    return BedrockModel(**model_config)


def create_enhanced_agent(
    tools: list,
    system_prompt: str,
    model_id: str = DEFAULT_MODEL_ID,
    region_name: str = DEFAULT_REGION,
    temperature: float = DEFAULT_TEMPERATURE,
    max_tokens: Optional[int] = None,
    **model_kwargs
) -> Agent:
    """
    Create an Agent with an enhanced BedrockModel configuration.
    
    Args:
        tools: List of tools for the agent
        system_prompt: System prompt for the agent
        model_id: The Bedrock model ID to use
        region_name: AWS region for Bedrock
        temperature: Temperature for model generation
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
        **model_kwargs
    )
    
    return Agent(
        model=bedrock_model,
        tools=tools,
        system_prompt=system_prompt,
    ) 