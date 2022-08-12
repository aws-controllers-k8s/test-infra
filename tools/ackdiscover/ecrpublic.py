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
import uuid

import boto3

BASE_ECR_URL = "public.ecr.aws/aws-controllers-k8s"
ECR_PUBLIC_AWS_ACCOUNT_ID = "628432846661"
ECR_PUBLIC_READER_ROLE_ARN = f"arn:aws:iam::{ECR_PUBLIC_AWS_ACCOUNT_ID}:role/ArtifactReader"


@dataclasses.dataclass
class AWSCredentials:
    access_key_id: str
    secret_access_key: str
    session_token: str


def _assume_reader_role(writer):
    """Returns an access key ID, secret access key, and a security token after
    assuming an ECR Public readonly role.
    """
    writer.debug("[ecr.assume_reader_role] assuming ECR Public ReadOnly role ...")
    role_session_name = str(uuid.uuid4())
    sts_client = boto3.client("sts")
    resp = sts_client.assume_role(
        RoleArn=ECR_PUBLIC_READER_ROLE_ARN,
        RoleSessionName=role_session_name,
        DurationSeconds=60*60,  # 1 hour...
    )
    creds = resp['Credentials']
    return AWSCredentials(
        access_key_id=creds['AccessKeyId'],
        secret_access_key=creds['SecretAccessKey'],
        session_token=creds['SessionToken'],
    )


def get_client(writer):
    """Returns the ECR Public client object after assuming the ECR Public
    Reader role.
    """
    aws_creds = _assume_reader_role(writer)

    # NOTE(jaypipes): ECR Public requires "us-east-1" when calling
    # DescribeRepositories...
    return boto3.client(
        "ecr-public",
        region_name="us-east-1",
        aws_access_key_id=aws_creds.access_key_id,
        aws_secret_access_key=aws_creds.secret_access_key,
        aws_session_token=aws_creds.session_token,
    )


@dataclasses.dataclass
class Repository:
    registry_id: str
    name: str
    uri: str
    created_on: str


def get_repository(writer, ep_client, repo_url):
    """Returns an Repository describing the supplied repository
    URL or None if no such repository exists.
    """
    writer.debug("[get_public_repository] ", repo_url)
    parts = repo_url.split("/")
    registry_id = ECR_PUBLIC_AWS_ACCOUNT_ID
    repo_name = parts[2]
    try:
        repos = ep_client.describe_repositories(
            registryId=registry_id,
            repositoryNames=[repo_name],
        )
        repo_data = repos["repositories"][0]
        return Repository(
            registry_id=registry_id,
            name=repo_name,
            uri=repo_data["repositoryUri"],
            created_on=repo_data["createdAt"],
        )
    except ep_client.exceptions.RepositoryNotFoundException:
        return None


def get_repository_latest_tag(ep_client, repo):
    """Returns the image tag of the latest pushed image in a supplied repo."""
    most_recent = None
    latest_tag = None
    next_token = None
    while True:
        try:
            kwargs = {
                'registryId': repo.registry_id,
                'repositoryName': repo.name,
                'maxResults': 100,
            }
            # sigh... boto doesn't deal with setting nextToken parameter to
            # None... so we need to simply not include it if nextToken is None.
            if next_token is not None:
                kwargs['nextToken'] = next_token
            images = ep_client.describe_images(**kwargs)
        except ep_client.exceptions.RepositoryNotFoundException:
            return latest_tag
        for image in images["imageDetails"]:
            if not 'imageTags' in image:
                continue
            pushed_at = image["imagePushedAt"]
            if most_recent is None or pushed_at > most_recent:
                most_recent = pushed_at
                latest_tag = image["imageTags"][0]
        if 'nextToken' in images:
            next_token = images['nextToken']
        if next_token is None:
            break
    return latest_tag


def chart_has_stable_tag(ep_client, chart_repo):
    """Returns True if the supplied chart repository has a chart with a stable
    tag, False otherwise.
    """
    try:
        images = ep_client.describe_images(
            registryId=chart_repo.registry_id,
            repositoryName=chart_repo.name,
        )
        for image in images["imageDetails"]:
            if "imageTags" not in image:
                continue
            tag = image["imageTags"][0]
            if "stable" in tag:
                return True
    except ep_client.exceptions.RepositoryNotFoundException:
        pass
    return False
