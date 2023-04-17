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

import boto3

from dataclasses import dataclass, field

from . import Bootstrappable
from .. import resources

@dataclass
class LogGroup(Bootstrappable):
    # Inputs
    name_prefix: str

    # Outputs
    name: str = field(init=False)
    arn: str = field(init=False)

    def __post_init__(self):
        self.name = resources.random_suffix_name(self.name_prefix, 63)

    @property
    def logs_client(self):
        return boto3.client("logs", region_name=self.region)
    
    @property
    def logs_resource(self):
        return boto3.resource("logs", region_name=self.region)
    
    def bootstrap(self):
        """Creates a CW Log group with an auto-generated name.
        """
        log_group = self.logs_client.create_log_group(
            logGroupName=self.name,
        )

        response = self.logs_client.describe_log_groups(
            logGroupNamePrefix=self.name,
        )

        self.arn = response["logGroups"][0]["arn"]
    
    def cleanup(self):
        """Deletes a CW Log group.
        """
        self.logs_client.delete_log_group(logGroupName=self.name)

