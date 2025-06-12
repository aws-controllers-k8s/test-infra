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
from strands import tool

from ack_builder_agent.prompts import ACK_BUILDER_SYSTEM_PROMPT
from ack_builder_agent.tools import build_controller, read_build_log, sleep, verify_build_completion
from ack_model_agent.prompt import ACK_MODEL_AGENT_SYSTEM_PROMPT
from ack_model_agent.tools import (
    save_operations_catalog,
    save_field_catalog,
    save_operation_analysis,
    save_error_catalog,
    save_resource_characteristics,
    query_knowledge_base,
)
from utils.docs_agent import DocsAgent
from utils.knowledge_base import retrieve_from_knowledge_base
from utils.memory_agent import MemoryAgent
from utils.repo import (
    ensure_ack_directories,
    ensure_aws_sdk_go_v2_cloned,
    ensure_service_repo_cloned,
    ensure_service_resource_directories,
)
from utils.settings import settings
from utils.bedrock import create_enhanced_agent

console = Console()
memory_agent = MemoryAgent()
docs_agent = DocsAgent()


@tool
def call_model_agent(service: str, resource: str) -> str:
    """
    Call the Model Agent to perform comprehensive AWS resource analysis.
    
    Args:
        service: AWS service name (e.g., 's3', 'ec2')
        resource: Resource name (e.g., 'Bucket', 'Instance')
        
    Returns:
        str: Model Agent analysis results
    """
    try:
        # Create the Model Agent with enhanced reliability settings
        model_agent = create_enhanced_agent(
            tools=[
                save_operations_catalog,
                save_field_catalog,
                save_operation_analysis,
                save_error_catalog,
                save_resource_characteristics,
                query_knowledge_base,
            ],
            system_prompt=ACK_MODEL_AGENT_SYSTEM_PROMPT,
        )
        
        # Call the model agent to analyze the resource
        query = f"Analyze AWS {service} {resource} resource"
        response = model_agent(query)
        
        return f"Model Agent analysis completed for {service} {resource}. Response: {str(response)}"
        
    except Exception as e:
        return f"Error calling Model Agent for {service} {resource}: {str(e)}"


@tool
def load_all_analysis_data(service: str, resource: str) -> str:
    """
    Load all 6 analysis files created by the Model Agent from the service/resource directory.
    
    Args:
        service: AWS service name (e.g., 's3', 'ec2')
        resource: Resource name (e.g., 'Bucket', 'Instance')
        
    Returns:
        str: JSON string containing all analysis data with filenames as keys
    """
    try:
        resource_dir = ensure_service_resource_directories(service, resource)
        analysis_data = {}
        
        # Define the 6 expected analysis files
        analysis_files = [
            "operations_catalog.json",
            "field_catalog.json", 
            "operation_analysis.json",
            "error_catalog.json",
            "resource_characteristics.json",
            "raw_analysis.txt"
        ]
        
        # Load each file
        for filename in analysis_files:
            file_path = os.path.join(resource_dir, filename)
            
            if os.path.exists(file_path):
                with open(file_path, 'r') as f:
                    content = f.read()
                    
                # For JSON files, parse and store as dict, for txt files store as string
                if filename.endswith('.json'):
                    try:
                        analysis_data[filename] = json.loads(content)
                    except json.JSONDecodeError:
                        analysis_data[filename] = content  # Store as string if JSON parsing fails
                else:
                    analysis_data[filename] = content
            else:
                analysis_data[filename] = f"File not found: {file_path}"
        
        # Return the analysis data as a formatted JSON string
        return json.dumps(analysis_data, indent=2)
        
    except Exception as e:
        return f"Error loading analysis data for {service} {resource}: {str(e)}"



@tool
def error_lookup(error_message: str) -> str:
    """
    Look up known solutions for a specific build error.
    
    Args:
        error_message: The error message to look up
        
    Returns:
        str: Known solution if found, or indication that no solution is known
    """
    return memory_agent.lookup_error_solution(error_message) or "No known solution found for this error."


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
def build_controller_agent(service: str) -> str:
    """
    Delegate the controller build process to the specialized builder agent.

    Args:
        service: Name of the AWS service (e.g., 's3', 'dynamodb')

    Returns:
        str: The builder agent's response (build status, logs, etc.)
    """
    try:
        builder_agent = create_enhanced_agent(
            tools=[build_controller, read_build_log, sleep, verify_build_completion],
            system_prompt=ACK_BUILDER_SYSTEM_PROMPT,
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
