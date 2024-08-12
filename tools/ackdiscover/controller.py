# Copyright Amazon.com Inc. or its affiliates. All Rights Reserved.
#
# Licensed under the Apache License, Version 2.0 (the "License"). You may
# not use this file except in compliance with the License. A copy of the
# License is located at
#
#	 http://aws.amazon.com/apache2.0/
#
# or in the "license" file accompanying this file. This file is distributed
# on an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either
# express or implied. See the License for the specific language governing
# permissions and limitations under the License.

import dataclasses
import os
import sys
import time

import github

from . import ecrpublic, maintenance_phases, project_stages, service

GITHUB_ORG_NAME = "aws-controllers-k8s"
GITHUB_ISSUE_REPO = "community"
SERVICE_CONTROLLER_LABEL = "kind/new-service"

# A cache of Github Issues for new service controllers
_sc_issues = None
# The Github Project for tracking service controllers
_sc_proj = None
# The project cards associated with the Planned column in the service
# controller Github Project
_sc_proj_planned_cards = None


@dataclasses.dataclass
class Release:
    controller_version: str = None
    chart_version: str = None
    ack_runtime_version: str = None
    aws_sdk_go_version: str = None
    release_url: str = None


@dataclasses.dataclass
class Controller:
    service: service.Service
    latest_release: Release
    project_stage: str = project_stages.NONE
    maintenance_phase: str = maintenance_phases.NONE
    source_repo_url: str = None
    image_repo: str = None
    chart_repo: str = None
    gh_issue_url: str = None


def collect_all(writer, gh, ep_client, services):
    """Returns a map, keyed by service package name, of ControllerInfo objects
    describing the ACK controllers.
    """
    writer.debug("[controller.collect_all] collecting ACK controller information ... ")
    ack_org = gh.get_organization(GITHUB_ORG_NAME)
    result= {}
    # Assume the ECR Public reader role
    ep_client = ecrpublic.get_client(writer)

    # Note that in role chaining, the session token is only valid for 1 hour
    # so we need to re-fetch the token for everytime we're close to the 1 hour
    # mark.
    now = time.time()

    for service_package_name, service in services.items():
        # If we're close to the 1 hour mark, re-fetch the ECR Public client
        if time.time() - now > 3480: # 3480 seconds == 58 minutes
            writer.debug("[controller.collect_all] re-fetching ECR Public client ...")
            ep_client = ecrpublic.get_client(writer)
            now = time.time()

        writer.debug(f"[controller.collect_all] finding controller info for {service_package_name} ...")

        project_stage = project_stages.NONE
        maintenance_phase = maintenance_phases.NONE
        # We check if there has been a GH issue created for the AWS service and
        # if that GH issue has been placed in the Service Controller Github
        # Project's "Planned" ProjectColumn.
        gh_issue = get_controller_request_issue(writer, gh, service)
        gh_issue_url = None
        if gh_issue is not None:
            gh_issue_url = gh_issue.html_url
            project_stage = project_stages.PROPOSED
            if is_planned(writer, gh, gh_issue):
                project_stage = project_stages.PLANNED

        try:
            repo = ack_org.get_repo(service_package_name+"-controller")
        except github.UnknownObjectException:
            controller = Controller(
                service=service,
                latest_release=None,
                project_stage=project_stage,
                maintenance_phase=maintenance_phase,
                source_repo_url=None,
                image_repo=None,
                chart_repo=None,
                gh_issue_url=gh_issue_url,
            )
            result[service_package_name] = controller
            continue

        latest_release = Release()

        image_repo_url = f"{ecrpublic.BASE_ECR_URL}/{service_package_name}-controller"
        image_repo_latest_version = None

        image_repo = ecrpublic.get_repository(writer, ep_client, image_repo_url)
        if image_repo:
            image_repo_latest_version = ecrpublic.get_repository_latest_tag(
                ep_client, image_repo,
            )
            latest_release.controller_version = image_repo_latest_version
            project_stage = project_stages.RELEASED
            maintenance_phase = maintenance_phases.PREVIEW
            runtime_version, aws_sdk_version = get_runtime_and_aws_sdk_version(
                writer, repo, image_repo_latest_version,
            )
            latest_release.ack_runtime_version = runtime_version
            latest_release.aws_sdk_go_version = aws_sdk_version
            
            try:
                gh_repo_release_version = image_repo_latest_version
                if not gh_repo_release_version.startswith("v"):
                    gh_repo_release_version = "v" + gh_repo_release_version
                gh_release = repo.get_release(gh_repo_release_version)
                latest_release.release_url = gh_release.html_url
            except github.UnknownObjectException:
                writer.debug(f"[controller.collect_all] no github release associated with controller version {gh_repo_release_version}")

        chart_repo_url = f"{ecrpublic.BASE_ECR_URL}/{service_package_name}-chart"
        chart_repo = ecrpublic.get_repository(writer, ep_client, chart_repo_url)
        if chart_repo:
            chart_repo_latest_version = ecrpublic.get_repository_latest_tag(
                ep_client, chart_repo,
            )
            latest_release.chart_version = chart_repo_latest_version
            if ecrpublic.chart_has_nonzero_major_version(ep_client, chart_repo):
                maintenance_phase = maintenance_phases.GENERAL_AVAILABILITY

        controller = Controller(
            service=service,
            latest_release=latest_release,
            project_stage=project_stage,
            maintenance_phase=maintenance_phase,
            source_repo_url=repo.html_url,
            image_repo=image_repo,
            chart_repo=chart_repo,
            gh_issue_url=gh_issue_url,
        )
        result[service_package_name] = controller
    return result


def get_runtime_and_aws_sdk_version(writer, repo, image_version):
    """Returns the ACK runtime and aws-sdk-go version used in the supplied
    controller's go.mod file at the specified image version (which is a Git tag
    on the repo...).
    """
    writer.debug("[controller.get_runtime_and_aws_sdk_version] fetching go.mod for", repo.name, "at Git tag", image_version)
    runtime_version = None
    aws_sdk_version = None
    try:
        go_mod_contents = repo.get_contents("go.mod", ref=image_version)
        for line in go_mod_contents.decoded_content.decode("utf-8").splitlines():
            parts = line.strip().split()
            if len(parts) != 2:
                continue
            if parts[0] == "github.com/aws-controllers-k8s/runtime":
                runtime_version = parts[1]
            elif parts[0] == "github.com/aws/aws-sdk-go":
                aws_sdk_version = parts[1]
    except github.GithubException:
        # NOTE(jaypipes): Some early controller repos like SNS/SQS don't have
        # proper Git tags for releases...
        pass
    return runtime_version, aws_sdk_version


def get_controller_request_issue(writer, gh, service):
    """Returns the Github Issue for the service, or None if no such issue
    exists.
    """
    global _sc_issues
    ack_org = gh.get_organization(GITHUB_ORG_NAME)
    community_repo = ack_org.get_repo(GITHUB_ISSUE_REPO)
    writer.debug(f"[controller.get_github_issue] finding Github Issue for {service.package_name} ...")
    if _sc_issues is None:
        sc_label = community_repo.get_label(name=SERVICE_CONTROLLER_LABEL)
        _sc_issues = community_repo.get_issues(labels=[sc_label])

    for issue in _sc_issues:
        # The GH issues with SERVICE_CONTROLLER_LABEL label all have the same title
        # pattern: "<Service Name> service controller"
        issue_title = issue.title.lower().replace("service controller", "")
        issue_title = issue_title.strip()
        if issue_title == service.package_name.lower():
            return issue
        if service.full_name is not None:
            full_name = service.full_name.lower()
            if issue_title == full_name:
                return issue
        if service.abbrev_name is not None:
            abbrev_name = service.abbrev_name.lower()
            if issue_title == abbrev_name:
                return issue
    return None


def get_service_controller_project(writer, gh):
    """Returns the GH project for tracking service controllers.
    """
    global _sc_proj
    ack_org = gh.get_organization(GITHUB_ORG_NAME)
    community_repo = ack_org.get_repo(GITHUB_ISSUE_REPO)
    writer.debug(f"[controller.get_service_controller_project] finding service controller Github Project ...")
    if _sc_proj is None:
        projs = community_repo.get_projects()
        for p in projs:
            if p.name == "Service Controller Release Roadmap":
                _sc_proj = p
                break
    return _sc_proj


def is_planned(writer, gh, gh_issue):
    """Returns whether the supplied GH issue for a service controller appears
    in the Planned board on our Service Controller Github Project.
    """
    global _sc_proj_planned_cards
    ack_org = gh.get_organization(GITHUB_ORG_NAME)
    community_repo = ack_org.get_repo(GITHUB_ISSUE_REPO)
    writer.debug(f"[controller.is_planned] looking up project card matching Github Issue {gh_issue.id} ...")
    if _sc_proj_planned_cards is None:
        sc_proj = get_service_controller_project(writer, gh)
        for pc in sc_proj.get_columns():
            if pc.name == "Planned":
                _sc_proj_planned_cards = pc.get_cards()
                break
    for pc in _sc_proj_planned_cards:
        if pc.content_url == gh_issue.url:
            return True

    return False
