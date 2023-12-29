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

    # The api-2.json file can be very large, and we only want to check the
    # metadata at the top of the file, so here we're going to just read a
    # portion of the file and manually search for the metadata fields that are
    # of interest to us.
    writer.debug("[get_service] fetching service information for:", model_name)
    api_model_path = os.path.join(model_path, api_version, "api-2.json")
    with open(api_model_path, "r") as model_file:
        # Assuming that the information we're looking for is very likely going to
        # be in the first 100 lines of the file...
        lines_number = 100
        head = [next(model_file) for _ in range(lines_number)]

        for line in head:
            parts = line.strip().split(":", 1)
            if len(parts) != 2:
                continue
            key, val = parts
            key = key.replace("\"", "").strip()
            # Strip the trailing comma...
            if val.endswith(","):
                val = val[:len(val)-1]
            val = val.replace("\"", "").strip()

            if key == "serviceFullName":
                result.full_name = val
            elif key == "serviceAbbreviation":
                result.abbrev_name = val
            if result.full_name != None and result.abbrev_name != None:
                break

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
