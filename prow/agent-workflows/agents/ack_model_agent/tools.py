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
"""ACK Model Agent tools for comprehensive AWS resource analysis."""

import json
import os
from typing import Dict, Any, List
from config.defaults import MODEL_AGENT_KB_NUMBER_OF_RESULTS, MODEL_AGENT_KB_SCORE_THRESHOLD
from strands import tool
from utils.knowledge_base import retrieve_from_knowledge_base
from utils.repo import ensure_ack_directories, ensure_service_resource_directories
from utils.settings import settings


@tool
def query_knowledge_base(query: str) -> str:
    """
    Query the knowledge base with optimized parameters for ACK analysis.
    
    Args:
        query (str): Search query string - should be comprehensive to maximize information
        
    Returns:
        str: Knowledge base results
    """
    if not settings.model_agent_kb_id:
        return "Error: MODEL_AGENT_KB_ID environment variable is not set"
    
    res = retrieve_from_knowledge_base(
        knowledgeBaseId=settings.model_agent_kb_id,
        text=query, 
        numberOfResults=MODEL_AGENT_KB_NUMBER_OF_RESULTS, 
        score=MODEL_AGENT_KB_SCORE_THRESHOLD  
    )
    return res


@tool
def save_operations_catalog(operations_catalog: Dict[str, List[str]], service: str, resource: str) -> str:
    """Save comprehensive catalog of all operations related to the resource.
    
    Args:
        operations_catalog: Dictionary with exactly these 5 keys:
            - create_operations: operation that create the resource
            - read_operations: operation that read the resource  
            - update_operations: operation that update the resource
            - delete_operations: operation that delete the resource
            - tag_operations: List of operations that manage tags on the resource
        service: AWS service name (e.g., 's3', 'dynamodb')
        resource: Resource name (e.g., 'Bucket', 'Table')
        
    Returns:
        str: Confirmation message with file path where catalog was saved
    """
    # Validate that operations_catalog has exactly the required 5 keys
    required_keys = {"create_operations", "read_operations", "update_operations", "delete_operations", "tag_operations"}
    provided_keys = set(operations_catalog.keys())
    
    if provided_keys != required_keys:
        missing_keys = required_keys - provided_keys
        extra_keys = provided_keys - required_keys
        error_msg = f"Operations catalog must have exactly these 5 keys: {required_keys}."
        if missing_keys:
            error_msg += f" Missing: {missing_keys}."
        if extra_keys:
            error_msg += f" Extra: {extra_keys}."
        return f"Error: {error_msg}"
    
    # Validate that first 4 keys have exactly 1 operation each
    single_operation_keys = {"create_operations", "read_operations", "update_operations", "delete_operations"}
    for key in single_operation_keys:
        if len(operations_catalog[key]) != 1:
            return f"Error: {key} must have exactly 1 operation, got {len(operations_catalog[key])}: {operations_catalog[key]}"
    
    ensure_ack_directories()
    resource_dir = ensure_service_resource_directories(service, resource)
    cache_file = os.path.join(resource_dir, "operations_catalog.json")
    with open(cache_file, 'w') as f:
        json.dump(operations_catalog, f, indent=2)
    return f"Operations catalog saved to {cache_file}"

@tool
def save_field_catalog(field_catalog: Dict[str, Dict], service: str, resource: str) -> str:
    """Save comprehensive catalog of all fields with their characteristics.
    
    Args:
        field_catalog: Dictionary mapping field names to their characteristics and metadata
        service: AWS service name (e.g., 's3', 'dynamodb')
        resource: Resource name (e.g., 'Bucket', 'Table')
        
    Returns:
        str: Confirmation message with file path where catalog was saved
    """
    ensure_ack_directories()
    resource_dir = ensure_service_resource_directories(service, resource)
    cache_file = os.path.join(resource_dir, "field_catalog.json")
    with open(cache_file, 'w') as f:
        json.dump(field_catalog, f, indent=2)
    return f"Field catalog saved to {cache_file}"

@tool  
def save_operation_analysis(operation_analysis: Dict[str, Any], service: str, resource: str) -> str:
    """Save detailed analysis of individual operations.
    
    Args:
        operation_analysis: Dictionary containing detailed analysis data for operations
        service: AWS service name (e.g., 's3', 'dynamodb')
        resource: Resource name (e.g., 'Bucket', 'Table')
        
    Returns:
        str: Confirmation message with file path where analysis was saved
    """
    ensure_ack_directories()
    resource_dir = ensure_service_resource_directories(service, resource)
    cache_file = os.path.join(resource_dir, "operation_analysis.json")
    with open(cache_file, 'w') as f:
        json.dump(operation_analysis, f, indent=2)
    return f"Operation analysis saved to {cache_file}"

@tool
def save_error_catalog(error_catalog: Dict[str, List], service: str, resource: str) -> str:
    """Save comprehensive error code analysis.
    
    Args:
        error_catalog: Dictionary mapping error types to lists of error codes and descriptions
        service: AWS service name (e.g., 's3', 'dynamodb')
        resource: Resource name (e.g., 'Bucket', 'Table')
        
    Returns:
        str: Confirmation message with file path where error catalog was saved
    """
    ensure_ack_directories()
    resource_dir = ensure_service_resource_directories(service, resource)
    cache_file = os.path.join(resource_dir, "error_catalog.json")
    with open(cache_file, 'w') as f:
        json.dump(error_catalog, f, indent=2)
    return f"Error catalog saved to {cache_file}"

@tool
def save_resource_characteristics(characteristics: Dict[str, Any], service: str, resource: str) -> str:
    """Save high-level resource behavior characteristics.
    
    Args:
        characteristics: Dictionary containing resource behavior and characteristics data
        service: AWS service name (e.g., 's3', 'dynamodb')
        resource: Resource name (e.g., 'Bucket', 'Table')
        
    Returns:
        str: Confirmation message with file path where characteristics were saved
    """
    ensure_ack_directories()
    resource_dir = ensure_service_resource_directories(service, resource)
    cache_file = os.path.join(resource_dir, "characteristics.json")
    with open(cache_file, 'w') as f:
        json.dump(characteristics, f, indent=2)
    return f"Resource characteristics saved to {cache_file}"
