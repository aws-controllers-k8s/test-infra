import boto3
import logging
import json
import re
import time

from dataclasses import dataclass, field
from typing import List

from . import Bootstrappable
from .. import resources
from ..aws.identity import get_region

# Regex to match the role name from a role ARN
ROLE_ARN_REGEX = r"^arn:aws:iam::\d{12}:(?:root|user|role\/([A-Za-z0-9-]+))$"

# Time to wait (in seconds) after a role is created
ROLE_CREATE_WAIT_IN_SECONDS = 3

@dataclass
class Role(Bootstrappable):
    # Inputs
    name_prefix: str
    principal_service: str
    policies: List[str]
    description: str = ""

    # Outputs
    arn: str = field(default="", init=False)
    
    @property
    def iam_client(self):
        return boto3.client("iam", region_name=self.region)

    def bootstrap(self):
        """Creates an IAM role with an auto-generated name.
        """
        role_name = resources.random_suffix_name(self.name_prefix, 63)

        self.iam_client.create_role(
            RoleName=role_name,
            AssumeRolePolicyDocument=json.dumps(
                {
                    "Version": "2012-10-17",
                    "Statement": [
                        {
                            "Effect": "Allow",
                            "Principal": {"Service": self.principal_service},
                            "Action": "sts:AssumeRole",
                        }
                    ],
                }
            ),
            Description=self.description,
        )

        for policy in self.policies:
            self.iam_client.attach_role_policy(
                RoleName=role_name,
                PolicyArn=policy,
            )

        iam_resource = self.iam_client.get_role(RoleName=role_name)
        resource_arn = iam_resource["Role"]["Arn"]

        # There appears to be a delay in role availability after role creation
        # resulting in failure that role is not present. So adding a delay
        # to allow for the role to become available
        time.sleep(ROLE_CREATE_WAIT_IN_SECONDS)

        self.arn = resource_arn

    def cleanup(self):
        """Deletes an IAM role.
        """
        role_name = re.match(ROLE_ARN_REGEX, self.arn).group(1)
        managed_policy = self.iam_client.list_attached_role_policies(RoleName=role_name)
        for each in managed_policy["AttachedPolicies"]:
            self.iam_client.detach_role_policy(RoleName=role_name, PolicyArn=each["PolicyArn"])

        inline_policy = self.iam_client.list_role_policies(RoleName=role_name)
        for each in inline_policy["PolicyNames"]:
            self.iam_client.delete_role_policy(RoleName=role_name, PolicyName=each)

        instance_profiles = self.iam_client.list_instance_profiles_for_role(RoleName=role_name)
        for each in instance_profiles["InstanceProfiles"]:
            self.iam_client.remove_role_from_instance_profile(
                RoleName=role_name, InstanceProfileName=each["InstanceProfileName"]
            )
        self.iam_client.delete_role(RoleName=role_name)