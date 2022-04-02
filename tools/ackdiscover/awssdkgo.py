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

import git


@dataclasses.dataclass
class Repo:
    path: str
    models_path: str
    service_path: str



def get_repo(writer, gh_token, root_dir):
    """Returns the Repo object representing a locally-checked-out Git clone
    of the aws-sdk-go repository.

    git clones the aws-sdk-go source repository into root_dir. If
    repository already exists, ensures it is up to latest commit.
    """
    upstream_url = f"https://{gh_token}:x-oath-basic@github.com/aws/aws-sdk-go"
    repos_path = os.path.join(root_dir, "repos")
    os.makedirs(repos_path, exist_ok=True)
    repo_path = os.path.join(repos_path, "aws-sdk-go")
    if not os.path.exists(repo_path):
        writer.debug("[ensure_aws_sdk_go] cloning aws-sdk-go repository ... ")
        git.Repo.clone_from(upstream_url, repo_path, depth=1)

    repo = git.Repo(repo_path)
    cached_commit = repo.head.commit.hexsha
    writer.debug("[ensure_aws_sdk_go] cached commit:", cached_commit)
    origin = repo.remote("origin")
    writer.debug("[ensure_aws_sdk_go] git pulling latest aws-sdk-go ... ")
    origin.pull()
    latest_commit = repo.head.commit.hexsha
    writer.debug("[ensure_aws_sdk_go] latest commit:", latest_commit)
    return Repo(
        path=repo_path,
        models_path= os.path.join(repo_path, "models", "apis"),
        service_path=os.path.join(repo_path, "service"),
    )
