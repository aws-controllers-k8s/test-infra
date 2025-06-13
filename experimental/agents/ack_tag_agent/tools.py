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
"""Tools for the ACK Tag Agent"""

import os
import subprocess
import logging
from pathlib import Path

from strands import tool
from utils.repo import ensure_ack_directories, ensure_service_repo_cloned, get_service_path
from utils.validation import is_path_under_directory

logger = logging.getLogger(__name__)


@tool
def write_service_controller_file(service: str, file_path: str, content: str) -> str:
    """
    Write contents to file in the service controller repo.

    Args:
    service: Name of the AWS service (e.g., 's3', 'dynamodb')
    file_path: Path of the file being written to.
    content: str - Content to be written to the file defined by file_path
    """
    logger.info(f"Writing tag hook to {file_path} for service {service}")
    service_path = get_service_path(service)
    tag_hook_path = os.path.join(service_path, file_path)
    if not is_path_under_directory(tag_hook_path, service_path):
        logger.error(f"SecurityViolation: Attempted to access file outside of service controller repo. {tag_hook_path}")
        return f"Error: {tag_hook_path} is not under {service_path}"

    # Create directory structure if it doesn't exist
    os.makedirs(os.path.dirname(tag_hook_path), exist_ok=True)

    # Remove the old generator.yaml if it exists
    if os.path.exists(tag_hook_path):
        os.remove(tag_hook_path)

    try:
        with open(tag_hook_path, "w") as f:
            f.write(content)
        return f"Tag hook successfully written to {tag_hook_path}"
    except Exception as e:
        print(f"Error writing tag hook to {tag_hook_path}: {str(e)}")
        return f"Error writing tag hook to {tag_hook_path}: {str(e)}"
    

@tool
def read_service_file(service: str, file_path: str) -> str:
    """
    Read the contents of a file in the specified service repository.

    Args:
    service: Name of the AWS service (e.g., 's3', 'dynamodb')
    file_path: Path of the file being read.

    Returns:
    str: Content of the file.
    """
    logger.info(f"Reading file {file_path} for service {service}")
    
    service_path = get_service_path(service)
    file_path = os.path.join(service_path, file_path)
    if not is_path_under_directory(file_path, service_path):
        logger.error(f"SecurityViolation: Attempted to access file outside of service controller repo. {file_path}")
        return f"Error: {file_path} is not under {service_path}"

    try:
        with open(file_path, "r") as f:
            return f.read()
    except Exception as e:
        return f"Error reading file {file_path}: {str(e)}"


@tool
def compile_service_controller(service: str) -> str:
    """
    Compile the Go controller for the specified service.

    Args:
    service: Name of the AWS service (e.g., 's3', 'dynamodb')

    Returns:
    str: Output of the Go compiler.
    """
    service_path = get_service_path(service)
    
    try:
        result = subprocess.run(
            ["go", "build", "cmd/controller/main.go"],
            capture_output=True,
            text=True,
            cwd=service_path
        )
        
        if result.returncode == 0:
            return f"Controller built successfully for {service} service."
        else:
            return f"Build failed for {service} service:\n{result.stderr}"
    except Exception as e:
        return f"Error building controller for {service} service: {str(e)}"
    

@tool
def run_tests_for_service_controller(service: str) -> str:
    """
    Run tests for the Go controller of the specified service.

    Args:
    service: Name of the AWS service (e.g., 's3', 'dynamodb')

    Returns:
    str: Output of the Go tests.
    """
    
    service_path = get_service_path(service)
    
    try:
        result = subprocess.run(
            ["make", "test"],
            capture_output=True,
            text=True,
            cwd=service_path
        )
        
        if result.returncode == 0:
            return f"Controller built successfully for {service} service."
        else:
            return f"Build failed for {service} service:\n{result.stderr}"
    except Exception as e:
        return f"Error building controller for {service} service: {str(e)}"