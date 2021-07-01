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
"""Handles PyTest resources and bootstrapping resource references.

PyTest resources are stored within the `resources` directory of each service
and contain YAML files used as templates for creating test fixtures.
"""

import string
import random
import yaml
from pathlib import Path
from typing import Any, Dict

from .aws import identity

def default_placeholder_values():
    """ Default placeholder values for loading any resource file.
    """
    return {
        "AWS_ACCOUNT_ID": identity.get_account_id(),
        "AWS_REGION": identity.get_region(),
    }

def load_resource_file(resources_directory: Path, resource_name: str,
                       additional_replacements: Dict[str, Any] = {}) -> dict:
    with open(resources_directory / f"{resource_name}.yaml", "r") as stream:
        resource_contents = stream.read()
        injected_contents = _replace_placeholder_values(
            resource_contents, default_placeholder_values())
        injected_contents = _replace_placeholder_values(
            injected_contents, additional_replacements)
        return yaml.safe_load(injected_contents)


def _replace_placeholder_values(
        in_str: str, replacement_dictionary: Dict[str, Any] = default_placeholder_values()) -> str:
    for placeholder, replacement in replacement_dictionary.items():
        in_str = in_str.replace(f"${placeholder}", replacement)
    return in_str


def random_suffix_name(resource_name: str, max_length: int,
                       delimiter: str = "-") -> str:
    rand_length = max_length - len(resource_name) - len(delimiter)
    rand = "".join(random.choice(string.ascii_lowercase + string.digits)
                   for _ in range(rand_length))
    return f"{resource_name}{delimiter}{rand}"
