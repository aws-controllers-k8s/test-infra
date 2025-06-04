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
"""ACK Codegen tools for Strands agents."""

import datetime
import os
import subprocess

import psutil
from rich.console import Console
from strands import tool

from config.defaults import DEFAULT_AWS_SDK_GO_VERSION, MAX_LOG_LINES_TO_RETURN
from utils.repo import (
    check_service_controller_exists,
    ensure_ack_directories,
    ensure_code_generator_cloned,
    ensure_runtime_cloned,
    ensure_service_repo_cloned,
)
from utils.settings import settings

console = Console()


def clean_tool_output(s: str) -> str:
    import re

    return re.sub(r"(\n\s*){3,}", "\n\n", (s or "").strip())


@tool
def build_controller(service: str, aws_sdk_version: str = DEFAULT_AWS_SDK_GO_VERSION) -> str:
    """Build a controller for a specific AWS service. This starts the build in the background.

    Args:
        service: AWS service name (e.g., 's3', 'dynamodb')
        aws_sdk_version: AWS SDK Go version

    Returns:
        str: Status message with log file information
    """
    try:
        ensure_ack_directories()
        console.log("Ensuring code generator is cloned...")
        ensure_code_generator_cloned()
        console.log("Ensuring runtime is cloned...")
        ensure_runtime_cloned()

        if not check_service_controller_exists(service):
            return f"Error: Service controller for {service} not found"

        console.log(f"Ensuring {service} controller is cloned...")
        service_path = ensure_service_repo_cloned(service)
        console.log(f"Service controller path: {service_path}")

        code_gen_path = settings.code_generator_path
        console.log(f"Using code generator at: {code_gen_path}")

        env = os.environ.copy()
        env.update(
            {
                "AWS_SDK_GO_VERSION": DEFAULT_AWS_SDK_GO_VERSION,
                "SERVICE": service,
                "RELEASE_VERSION": "v0.0.0-non-release-version",
            }
        )

        timestamp = datetime.datetime.now().strftime("%Y%m%d_%H%M%S")
        build_logs_dir = settings.build_logs_dir

        stdout_log_path = os.path.join(build_logs_dir, f"build_{service}_{timestamp}.stdout.log")
        stderr_log_path = os.path.join(build_logs_dir, f"build_{service}_{timestamp}.stderr.log")

        console.log(f"Starting background build for {service}. This may take a few minutes.")
        console.log(f"Stdout will be logged to: {stdout_log_path}")
        console.log(f"Stderr will be logged to: {stderr_log_path}")

        build_cmd = f"make build-controller SERVICE=$SERVICE"

        # Open log files
        stdout_log_file = open(stdout_log_path, "w")
        stderr_log_file = open(stderr_log_path, "w")

        # Use subprocess.Popen for background execution instead of asyncio
        process = subprocess.Popen(
            build_cmd,
            cwd=code_gen_path,
            env=env,
            stdout=stdout_log_file,
            stderr=stderr_log_file,
            shell=True,
        )

        return clean_tool_output(
            f"Build for {service} started in the background. PID: {process.pid}. Logs: {stdout_log_path}, {stderr_log_path}"
        )

    except Exception as e:
        console.log(f"Exception during build setup: {str(e)}")
        return clean_tool_output(f"Error during build setup: {str(e)}")


@tool
def read_build_log(log_file_name: str) -> str:
    """Reads the last N lines from a specified build log file.

    Args:
        log_file_name: The name of the log file (e.g., 'build_s3_20231027_123456.stdout.log').

    Returns:
        str: Log contents or error message
    """
    console.log(f"Attempting to read log file: {log_file_name}")
    build_logs_dir = settings.build_logs_dir
    target_log_path = os.path.join(build_logs_dir, log_file_name)

    if not os.path.exists(target_log_path):
        return f"Error: Log file not found: {target_log_path}"

    try:
        with open(target_log_path, "r") as f:
            lines = f.readlines()

        if not lines:
            return f"Log file is empty: {target_log_path}"

        num_lines_to_return = min(len(lines), MAX_LOG_LINES_TO_RETURN)
        start_index = len(lines) - num_lines_to_return
        content_to_return = "".join(lines[start_index:])

        header = f"--- Last {num_lines_to_return} lines of {log_file_name} ---\n"
        if len(lines) > MAX_LOG_LINES_TO_RETURN:
            header += f"(Log file has {len(lines)} total lines. Displaying the last {MAX_LOG_LINES_TO_RETURN}.)\n"

        return clean_tool_output(header + content_to_return)
    except Exception as e:
        console.log(f"Error reading log file {target_log_path}: {str(e)}")
        return clean_tool_output(f"Error reading log file {target_log_path}: {str(e)}")


@tool
def sleep(seconds: int) -> str:
    """Pauses execution for the specified number of seconds.

    Args:
        seconds: Number of seconds to sleep/wait

    Returns:
        str: Confirmation message after sleeping
    """
    if seconds <= 0:
        return clean_tool_output("Error: Sleep duration must be a positive number")

    if seconds > 600:  # Limit maximum sleep time to 10 minutes
        return clean_tool_output("Error: Maximum sleep duration is 600 seconds (10 minutes)")

    console.log(f"Sleeping for {seconds} seconds...")

    import time

    time.sleep(seconds)

    return clean_tool_output(f"Successfully slept for {seconds} seconds")


@tool
def verify_build_completion(pid: int) -> str:
    """Verifies if a build process with the specified PID is still running."""
    if pid <= 0:
        return clean_tool_output("Error: Invalid PID provided")
    try:
        p = psutil.Process(pid)
        if p.is_running() and p.status() != psutil.STATUS_ZOMBIE:
            return clean_tool_output(f"Process with PID {pid} is still running")
        else:
            return clean_tool_output(f"Process with PID {pid} has completed or was terminated")
    except psutil.NoSuchProcess:
        return clean_tool_output(f"Process with PID {pid} has completed or was terminated")
    except Exception as e:
        return clean_tool_output(f"Error checking process status: {str(e)}")
