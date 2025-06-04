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
"""Repository utility functions for ACK agents."""

import os

import requests
import yaml
from git import Repo

from utils.settings import settings


def ensure_ack_directories():
    """Create the ACK directory structure."""
    os.makedirs(settings.ack_root, exist_ok=True)
    os.makedirs(settings.build_logs_dir, exist_ok=True)


def fetch_all_tags(repo_path: str):
    """Fetch all tags for a given git repository path."""
    try:
        repo = Repo(repo_path)
        repo.remotes.origin.fetch(tags=True)
        print(f"Fetched all tags for repo at {repo_path}")
    except Exception as e:
        print(f"Warning: Could not fetch tags for {repo_path}: {e}")


def safe_pull(origin):
    try:
        pull_result = origin.pull(rebase=True)
        # If pull_result is a list of FetchInfo, check summary
        if pull_result and hasattr(pull_result[0], "summary"):
            summary = pull_result[0].summary.lower()
            if "up to date" in summary or "already up to date" in summary:
                print("Already up to date.")
                return
    except Exception as e:
        if "up to date" in str(e).lower() or "no changes" in str(e).lower():
            print("Already up to date.")
        else:
            print(f"Warning: Could not pull latest changes: {e}")
            print("Continuing with existing code...")


def ensure_code_generator_cloned() -> str:
    """Clone or update the ACK code generator repository.

    Returns:
        Path to the local code generator repository
    """
    ensure_ack_directories()

    if not os.path.exists(settings.code_generator_path):
        print(f"Cloning code generator from {settings.code_generator_url}...")
        Repo.clone_from(settings.code_generator_url, settings.code_generator_path)

    fetch_all_tags(settings.code_generator_path)
    return settings.code_generator_path


def ensure_runtime_cloned() -> str:
    """Clone or update the ACK runtime repository.

    Returns:
        Path to the local runtime repository
    """
    ensure_ack_directories()

    if not os.path.exists(settings.runtime_path):
        print(f"Cloning runtime from {settings.runtime_url}...")
        Repo.clone_from(settings.runtime_url, settings.runtime_path)

    fetch_all_tags(settings.runtime_path)
    return settings.runtime_path


def ensure_aws_sdk_go_v2_cloned() -> str:
    """Clone or update the aws-sdk-go-v2 repository.

    Returns:
        Path to the local aws-sdk-go-v2 repository
    """
    ensure_ack_directories()

    if not os.path.exists(settings.aws_sdk_go_v2_path):
        print(f"Cloning aws-sdk-go-v2 from {settings.aws_sdk_go_v2_url}...")
        Repo.clone_from(settings.aws_sdk_go_v2_url, settings.aws_sdk_go_v2_path)

    fetch_all_tags(settings.aws_sdk_go_v2_path)
    return settings.aws_sdk_go_v2_path


def check_service_controller_exists(service: str) -> bool:
    """Check if a service controller exists in the ACK organization.

    Args:
        service: Name of the AWS service

    Returns:
        True if service controller exists
    """
    repo_url = f"{settings.ack_org_url}/{service}-controller"
    response = requests.head(repo_url)
    return response.status_code == 200


def ensure_service_repo_cloned(service: str) -> str:
    """Clone or update a service controller repository.

    Args:
        service: Name of the AWS service

    Returns:
        Path to the local service controller repository
    """
    ensure_ack_directories()
    check_service_controller_exists(service)
    service_path = settings.get_controller_path(service)

    if not os.path.exists(service_path):
        print(f"Cloning {service} controller repository...")
        Repo.clone_from(f"{settings.ack_org_url}/{service}-controller", service_path)

    fetch_all_tags(service_path)
    return service_path


def get_release_version(service_path: str) -> str:
    """Get the release version from the Helm chart.

    Args:
        service_path: Path to the service controller repository

    Returns:
        Version string, defaults to '0.0.1' if not found
    """
    chart_path = os.path.join(service_path, "helm", "Chart.yaml")
    try:
        with open(chart_path, "r") as f:
            chart_data = yaml.safe_load(f)
            return chart_data.get("version", "0.0.1")
    except (FileNotFoundError, yaml.YAMLError):
        return "0.0.1"
