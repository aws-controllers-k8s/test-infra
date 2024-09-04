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
import ijson
import json

@dataclasses.dataclass
class Service:
    model_name: str
    api_version: str
    full_name: str = None
    abbrev_name: str = None
    package_name: str = None

def collect_all(writer, repo):
    """Returns a map, keyed by AWS *service package* name, of ServiceInfo
    objects describing the AWS services callable via aws-sdk-go.
    """
    writer.debug("[collect_services] collecting AWS service information ... ")
    result = {}
    for fname in os.listdir(repo.models_path):
        if not os.path.isdir(os.path.join(repo.models_path, fname)):
            continue

        service = get(writer, repo, fname)
        if service is None:
            writer.debug("[collect_services] skipping service:", fname)
            continue
        result[service.package_name] = service

    return result


def get(writer, repo, model_name):
    """Returns a Service describing the service with the supplied model
    name.
    """
    model_path = os.path.join(repo.models_path, model_name)
    # All top-level filepaths are directories where the directory contains a
    # single subdirectory containing the AWS service's current API version --
    # e.g.  s3/2006-03-01
    #
    # Within this subdirectory will be an api-2.json containing the API's model
    # definition including metadata about the service.
    api_version = [fname for fname in os.listdir(model_path)][0]

    result = Service(
        model_name=model_name,
        api_version=api_version,
    )

    # The api-2.json file can be fairly large (see ec2 api-2.JSON), but it is what it
    # is. We're going to load it into memory and parse it as JSON.
    writer.debug("[get_service] fetching service information for:", model_name)
    api_model_path = os.path.join(model_path, api_version, "api-2.json")
    with open(api_model_path, "r") as model_file:
        metadata = ijson.items(model_file, "metadata")
        if metadata is None:
            return None

        for obj in metadata:
            result.full_name = obj.get("serviceFullName")
            result.abbrev_name = obj.get("serviceAbbreviation")

        pkg_name = package_name(result.abbrev_name, result.full_name)
        result.package_name = pkg_name

    return result


# Emulates the code in
# https://github.com/aws/aws-sdk-go/blob/93134df4fd5d1cdb72dd122a850b54d414792d71/private/model/api/api.go#L124-L162
# to determine the name of the service package...
def package_name(abbrev, full_name):
    pkg_name = abbrev
    if pkg_name is None:
        pkg_name = full_name
    if pkg_name.startswith("AWS"):
        pkg_name = pkg_name[3:]
    if pkg_name.startswith("Amazon"):
        pkg_name = pkg_name[6:]
    pkg_name = pkg_name.replace(" ", "")
    return pkg_name.lower()
