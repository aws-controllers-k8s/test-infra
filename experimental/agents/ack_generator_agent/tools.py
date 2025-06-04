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
"""ACK Generator tools for Strands agents."""

import json
import os

from rich.console import Console
from strands import Agent, tool

from ack_builder_agent.prompts import ACK_BUILDER_SYSTEM_PROMPT
from ack_builder_agent.tools import build_controller, read_build_log, sleep, verify_build_completion
from utils.docs_agent import DocsAgent
from utils.knowledge_base import retrieve_from_knowledge_base
from utils.memory_agent import MemoryAgent
from utils.repo import (
    ensure_ack_directories,
    ensure_aws_sdk_go_v2_cloned,
    ensure_service_repo_cloned,
)
from utils.settings import settings

console = Console()
memory_agent = MemoryAgent()
docs_agent = DocsAgent()


@tool
def read_service_generator_config(service: str) -> str:
    """Reads the generator.yaml file from a service controller directory.

    Args:
        service: Name of the AWS service (e.g., 's3', 'dynamodb')

    Returns:
        str: Content of the generator.yaml file or an error message.
    """
    try:
        ensure_ack_directories()
        service_path = ensure_service_repo_cloned(service)

        generator_config_path = os.path.join(service_path, "generator.yaml")

        if not os.path.exists(generator_config_path):
            return f"Error: generator.yaml not found in {service_path}"

        with open(generator_config_path, "r") as f:
            content = f.read()
        return content
    except Exception as e:
        return f"Error reading generator.yaml for {service}: {str(e)}"


@tool
def read_service_model(service: str) -> str:
    """Reads the AWS service model JSON file from the code generator repository.

    Args:
        service: Name of the AWS service (e.g., 's3', 'dynamodb')

    Returns:
        str: Content of the service model JSON file or an error message.
    """
    try:
        # Make sure AWS SDK is cloned
        ensure_ack_directories()
        ensure_aws_sdk_go_v2_cloned()

        # Get the model file path from settings
        model_path = settings.get_aws_service_model_path(service)

        console.log(f"Looking for model at: {model_path}")

        if not os.path.exists(model_path):
            return f"Error: Model file not found for service '{service}' at {model_path}"

        with open(model_path, "r") as f:
            content = f.read()

        # Parse and return the full model content
        try:
            data = json.loads(content)
            return json.dumps(data, indent=2)
        except Exception as e:
            console.log(f"Error parsing model JSON: {str(e)}")
            return content

    except Exception as e:
        return f"Error reading service model for {service}: {str(e)}"


@tool
def build_controller_agent(service: str) -> str:
    """
    Delegate the controller build process to the specialized builder agent.

    Args:
        service: Name of the AWS service (e.g., 's3', 'dynamodb')

    Returns:
        str: The builder agent's response (build status, logs, etc.)
    """
    try:
        builder_agent = Agent(
            system_prompt=ACK_BUILDER_SYSTEM_PROMPT,
            tools=[build_controller, read_build_log, sleep, verify_build_completion],
        )
        response = builder_agent(service)
        return str(response)
    except Exception as e:
        return f"Error in build_controller_agent: {str(e)}"


# TODO(rushmash91): This is a temporary tool to update the generator.yaml file.
# this will need a lot of checks and possibly a generator.yaml validator tool too
@tool
def update_service_generator_config(service: str, new_generator_yaml: str) -> str:
    """
    Replace the generator.yaml file for a given service controller with new content.

    Args:
        service: Name of the AWS service (e.g., 's3', 'dynamodb')
        new_generator_yaml: The full new content for generator.yaml

    Returns:
        str: Success or error message
    """
    try:
        ensure_ack_directories()
        service_path = ensure_service_repo_cloned(service)
        generator_config_path = os.path.join(service_path, "generator.yaml")

        # Remove the old generator.yaml if it exists
        if os.path.exists(generator_config_path):
            os.remove(generator_config_path)

        # Write the new generator.yaml
        with open(generator_config_path, "w") as f:
            f.write(new_generator_yaml)

        return f"Successfully updated generator.yaml for {service} at {generator_config_path}"
    except Exception as e:
        return f"Error updating generator.yaml for {service}: {str(e)}"


@tool
def add_memory(content: str, metadata: dict) -> str:
    """
    Manually add a memory to the agent's memory store.

    Args:
        content: The content/information to store in memory
        metadata: Optional metadata dictionary to associate with the memory

    Returns:
        str: Success or error message
    """
    return memory_agent.add_knowledge(content, metadata)


@tool
def search_memories(query: str, limit: int = 5, min_score: float = 0.5) -> str:
    """
    Search through stored memories using a query.

    Args:
        query: The search query to find relevant memories
        limit: Maximum number of memories to return (default: 5)
        min_score: Minimum relevance score for results (default: 0.5)

    Returns:
        str: Found memories or message if none found
    """
    return memory_agent.search_memories(query, limit)


@tool
def list_all_memories() -> str:
    """
    List all stored memories in the agent's memory.

    Returns:
        str: All stored memories with metadata
    """
    return memory_agent.list_all_memories()


# TODO(rushmash91): This is lookup to look up code-generator configs/ if we have a validator might not be needed
@tool
def lookup_code_generator_config(service: str, resource: str) -> str:
    """
    Look up the code-generator config for a given service and resource.
    """
    return f""


@tool
def save_error_solution(error_message: str, solution: str, metadata: dict) -> str:
    """
    Save an error message and its solution to the agent's memory.

    Args:
        error_message: The error message to save
        solution: The solution for this error

    Returns:
        str: Success or error message
    """
    return memory_agent.store_error_solution(error_message, solution, metadata)


@tool
def search_codegen_knowledge(query: str, numberOfResults: int = 5) -> str:
    """
    Search for code generation related information in the knowledge base.

    This is a specialized version of retrieve_from_knowledge_base that's optimized
    for searching code generation patterns, configurations, and best practices.

    Args:
        query: The search query focused on code generation topics
        numberOfResults: Maximum number of results to return (default: 5)

    Returns:
        str: Code generation related information or error message
    """

    return retrieve_from_knowledge_base(
        text=query,
        numberOfResults=numberOfResults,
        score=0.6,  # Higher threshold for more relevant results
        knowledgeBaseId="",
    )


@tool
def search_aws_documentation(query: str, max_results: int = 5) -> str:
    """
    Search AWS documentation using the AWS documentation MCP server.

    This tool provides access to comprehensive AWS service documentation,
    API references, user guides, and best practices.

    Args:
        query: Search query for AWS documentation (e.g., "S3 bucket configuration", "DynamoDB table creation")
        max_results: Maximum number of documentation results to return (default: 5)

    Returns:
        str: AWS documentation search results or error message
    """
    return docs_agent.search_documentation(query, max_results)


@tool
def read_aws_documentation(url: str, max_length: int = 5000, start_index: int = 0) -> str:
    """
    Read specific AWS documentation page content.

    Args:
        url: AWS documentation URL (must be from docs.aws.amazon.com)
        max_length: Maximum number of characters to return (default: 5000)
        start_index: Starting character index for partial reads (default: 0)

    Returns:
        str: AWS documentation page content in markdown format or error message
    """
    return docs_agent.read_documentation_page(url, max_length, start_index)


@tool
def get_aws_documentation_recommendations(url: str) -> str:
    """
    Get content recommendations for an AWS documentation page.

    This tool provides recommendations for related AWS documentation pages
    including highly rated, new, similar, and commonly viewed next pages.

    Args:
        url: AWS documentation URL to get recommendations for

    Returns:
        str: List of recommended documentation pages with URLs, titles, and context
    """
    return docs_agent.get_documentation_recommendations(url)


@tool
def find_service_documentation(service: str, resource: str) -> str:
    """
    Find AWS service documentation specifically for ACK controller generation.

    This tool searches for AWS service API documentation, user guides, and best practices
    that are relevant for generating Kubernetes controllers.

    Args:
        service: AWS service name (e.g., 's3', 'dynamodb', 'rds')
        resource: Optional specific resource name (e.g., 'bucket', 'table', 'cluster')

    Returns:
        str: Relevant AWS documentation for the service/resource or error message
    """
    return docs_agent.find_service_documentation(service, resource)
