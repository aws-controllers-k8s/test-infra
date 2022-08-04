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
class SigningProfile(Bootstrappable):
    # Inputs
    name_prefix: str
    signing_platform_id: str

    # Outputs
    signing_profile_arn: str = field(init=False)
    
    @property
    def signer_client(self):
        return boto3.client("signer", region_name=self.region)

    def bootstrap(self):
        """Creates a Signing profile with a generated name
        """
        self.name = resources.random_suffix_name(self.name_prefix, 32, delimiter="_")
        signing_profile = self.signer_client.put_signing_profile(
            profileName=self.name,
            platformId=self.signing_platform_id,
        )
        self.signing_profile_arn = signing_profile['profileVersionArn']

    def cleanup(self):
        """Cancels the signing profile.
        """
        self.signer_client.cancel_signing_profile(
            profileName=self.name,
        )