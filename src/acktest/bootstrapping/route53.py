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
class HealthCheck(Bootstrappable):
    # Inputs
    caller_reference_prefix: str
    health_check_config: dict

    # Outputs
    id: str = field(init=False)
    location: str = field(init=False)

    @property
    def route53_client(self):
        return boto3.client("route53", region_name=self.region)

    def bootstrap(self):
        """Creates a Route53 HealthCheck.
        """
        self.caller_reference = resources.random_suffix_name(self.caller_reference_prefix, 63)
        health_check = self.route53_client.create_health_check(
            CallerReference=self.caller_reference,
            HealthCheckConfig=self.health_check_config,
        )
        self.location = health_check["Location"]
        self.id = health_check["HealthCheck"]["Id"]

    def cleanup(self):
        """Deletes a Route53 HealthCheck
        """
        self.route53_client.delete_health_check(
            HealthCheckId=self.id,
        )
