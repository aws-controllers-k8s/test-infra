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

import github

import ecrpublic
import maintenance_phases
import project_stages
import service


@dataclasses.dataclass
class Release:
    controller_version: str = None
    chart_version: str = None
    ack_runtime_version: str = None
    aws_sdk_go_version: str = None


@dataclasses.dataclass
class Controller:
    service: service.Service
    latest_release: Release
    project_stage: str = project_stages.NONE
    maintenance_phase: str = maintenance_phases.NONE
    source_repo_url: str = None
    image_repo: str = None
    chart_repo: str = None


def collect_all(writer, gh, ep_client, services):
    """Returns a map, keyed by service package name, of ControllerInfo objects
    describing the ACK controllers.
    """
    writer.debug("[collect_controllers] collecting ACK controller information ... ")
    ack_org = gh.get_organization("aws-controllers-k8s")
    result= {}

    for service_package_name, service in services.items():
        writer.debug(f"[collect_controllers] finding controller info for {service_package_name} ...")
        try:
            repo = ack_org.get_repo(service_package_name+"-controller")
        except github.UnknownObjectException:
            controller = Controller(
                service=service,
                latest_release=None,
                project_stage=project_stages.NONE,
                maintenance_phase=maintenance_phases.NONE,
                source_repo_url=None,
                image_repo=None,
                chart_repo=None,
            )
            result[service_package_name] = controller
            continue

        latest_release = Release()
        project_stage = project_stages.NONE
        maintenance_phase = maintenance_phases.NONE

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

        chart_repo_url = f"{ecrpublic.BASE_ECR_URL}/{service_package_name}-chart"
        chart_repo = ecrpublic.get_repository(writer, ep_client, chart_repo_url)
        if chart_repo:
            chart_repo_latest_version = ecrpublic.get_repository_latest_tag(
                ep_client, chart_repo,
            )
            latest_release.chart_version = chart_repo_latest_version
            if ecrpublic.chart_has_stable_tag(ep_client, chart_repo):
                maintenance_phase = maintenance_phases.GENERAL_AVAILABILITY

        controller = Controller(
            service=service,
            latest_release=latest_release,
            project_stage=project_stage,
            maintenance_phase=maintenance_phase,
            source_repo_url=repo.url,
            image_repo=image_repo,
            chart_repo=chart_repo,
        )
        result[service_package_name] = controller
    return result


def get_runtime_and_aws_sdk_version(writer, repo, image_version):
    """Returns the ACK runtime and aws-sdk-go version used in the supplied
    controller's go.mod file at the specified image version (which is a Git tag
    on the repo...).
    """
    writer.debug("[get_runtime_and_aws_sdk_version] fetching go.mod for", repo.name, "at Git tag", image_version)
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
