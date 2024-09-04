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
import requests

import github

from . import ecrpublic, maintenance_phases, project_stages, service

GITHUB_ORG_NAME = "aws-controllers-k8s"
GITHUB_ISSUE_REPO = "community"
SERVICE_CONTROLLER_LABEL = "kind/new-service"

# When we first started the ACK project, all of the service controller
# repositories were named with the pattern "<package-name>-controller".
# However, as we've added more services, we've started to use a different
# and learned that some services have different names in the AWS SDKs
# than they do in their model files... This map is used to map the
# package name to the repository name.
#
# NOTE(a-hilaly): This is a temporary solution until we have a better way
# to handle this. I originally want to use some sort of metadata in the
# Github issues in the style of /model-name /controller-name but that
# would be risky as users can change the metadata, and unwilingly (or
# willingly) break the system.
exceptional_service_names = {
    "eventbridgepipes": {
        "controller_name": "pipes",
    },
    "docdb": {
        "controller_name": "documentdb",
    },
    "elasticloadbalancingv2": {
        "controller_name": "elbv2",
    },
    "acm-pca": {
        "controller_name": "acmpca",
    },
}

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


def collect_all(writer, gh, services):
    """Returns a map, keyed by service package name, of ControllerInfo objects
    describing the ACK controllers.
    """
    writer.debug("[controller.collect_all] collecting ACK controller information ... ")
    ack_org = gh.get_organization(GITHUB_ORG_NAME)
    result= {}
    project_data = fetch_project_data(writer)
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
        if service.package_name in exceptional_service_names:
            writer.debug(f"[controller.collect_all] replacing service package name {service.package_name} with {exceptional_service_names[service.package_name]['controller_name']}")
            service.package_name = exceptional_service_names[service.package_name]["controller_name"]
            service_package_name = service.package_name

        gh_issue = find_issue_for_service(project_data, service)
        gh_issue_url = None
        if gh_issue:
            gh_issue_url = gh_issue['url']
            project_stage = project_stages.PROPOSED
            if gh_issue['status'] == 'Planned':
                project_stage = project_stages.PLANNED

        try:
            repo = ack_org.get_repo(service_package_name + "-controller")
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

def fetch_project_data(writer):
    """Fetches project data using GraphQL API..."""
    writer.debug("[controller.fetch_project_data] fetching project data using GraphQL...")
    url = 'https://api.github.com/graphql'
    token = os.getenv('GITHUB_TOKEN')
    
    query = '''
    query {
      organization(login: "aws-controllers-k8s") {
        projectV2(number: 10) {
          id
          title
          number
          items(first: 100) {
            nodes {
              id
              type
              content {
                ... on Issue {
                  title
                  body
                  number
                  url
                  repository {
                    name
                  }
                }
              }
              fieldValues(first: 100) {
                nodes {
                  ... on ProjectV2ItemFieldSingleSelectValue {
                    name
                    field {
                      ... on ProjectV2FieldCommon {
                        name
                      }
                    }
                  }
                }
              }
            }
          }
        }
      }
    }
    '''

    headers = {
        'Authorization': f'Bearer {token}',
        'Content-Type': 'application/json',
    }

    response = requests.post(url, json={'query': query}, headers=headers)
    
    if response.status_code == 200:
        return response.json()['data']['organization']['projectV2']['items']['nodes']
    else:
        writer.error(f"Failed to fetch project data: {response.status_code}")
        return []

def find_issue_for_service(project_data, service):
    """Finds the issue for the given service in the project data"""
    for item in project_data:
        if item['type'] == 'ISSUE' and item['content']:
            issue_title = item['content']['title'].lower()
            
            # Remove "service controller" from the title and strip whitespace
            issue_title = issue_title.replace("service controller", "").strip()
            
            # Check against package name, full name, and abbreviated name
            if issue_title == service.package_name.lower():
                return process_issue(item)
            
            if service.full_name is not None:
                full_name = service.full_name.lower()
                if issue_title == full_name:
                    return process_issue(item)
            
            if service.abbrev_name is not None:
                abbrev_name = service.abbrev_name.lower()
                if issue_title == abbrev_name:
                    return process_issue(item)
    
    return None

def process_issue(item):
    """Returns a dictionary with the relevant information from the GH issue"""
    status = 'Unknown'
    for field_value in item['fieldValues']['nodes']:
        if field_value and 'field' in field_value and field_value['field']['name'] == 'Status':
            status = field_value['name']
            break
    
    return {
        'id': item['id'],
        'title': item['content']['title'],
        'body': item['content']['body'],
        'number': item['content']['number'],
        'url': item['content']['url'],
        'status': status
    }
