# Copyright Amazon.com Inc. or its affiliates. All Rights Reserved.
#
# Licensed under the Apache License, Version 2.0 (the "License"). You may
# not use this file except in compliance with the License. A copy of the
# License is located at
#
# http://aws.amazon.com/apache2.0/
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
class UserPool(Bootstrappable):
    """Represents a Cognito User Pool bootstrapped resource."""
    # Inputs
    name_prefix: str

    # Outputs
    name: str = field(init=False)
    user_pool_id: str = field(init=False)
    user_pool_arn: str = field(init=False)

    def __post_init__(self):
        self.name = resources.random_suffix_name(self.name_prefix, 63)

    @property
    def cognito_idp_client(self):
        return boto3.client("cognito-idp", region_name=self.region)

    def bootstrap(self):
        """Creates a Cognito User Pool with an auto-generated name."""
        resp = self.cognito_idp_client.create_user_pool(PoolName=self.name)
        self.user_pool_id = resp["UserPool"]["Id"]
        self.user_pool_arn = resp["UserPool"]["Arn"]

    def cleanup(self):
        """Deletes the Cognito User Pool."""
        self.cognito_idp_client.delete_user_pool(UserPoolId=self.user_pool_id)
