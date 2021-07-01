import boto3
import logging
import json
import re
import time

from dataclasses import dataclass, field
from typing import List

from . import BootstrappableResource
from .. import resources
from ..aws.identity import get_region

# Regex to match the role name from a role ARN
ROLE_ARN_REGEX = r"^arn:aws:iam::\d{12}:(?:root|user|role\/([A-Za-z0-9-]+))$"

# Time to wait (in seconds) after a role is created
ROLE_CREATE_WAIT_IN_SECONDS = 3

@dataclass
class Role(BootstrappableResource):
    # Inputs
    name_prefix: str
    principal_service: str
    policies: List[str]
    description: str = ""

    # Outputs
    arn: str = field(default="", init=False)
    
    def bootstrap(self):
        """Creates an IAM role with an auto-generated name.

        Args:
            name_prefix (str): The prefix for the auto-generated name.
            principal_service (str): The service principal that is allowed to assume
                the role.
            policies (List[str]): A list of IAM policy ARNs that are attached to the
                the role.
            description (str, optional): The role description. Defaults to "".
        """
        region = get_region()
        role_name = resources.random_suffix_name(self.name_prefix, 63)
        iam = boto3.client("iam", region_name=region)

        iam.create_role(
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
            iam.attach_role_policy(
                RoleName=role_name,
                PolicyArn=policy,
            )

        iam_resource = iam.get_role(RoleName=role_name)
        resource_arn = iam_resource["Role"]["Arn"]

        # There appears to be a delay in role availability after role creation
        # resulting in failure that role is not present. So adding a delay
        # to allow for the role to become available
        time.sleep(ROLE_CREATE_WAIT_IN_SECONDS)
        logging.info(f"Created role {resource_arn}")

        self.arn = resource_arn

    def cleanup(self):
        """Deletes an IAM role.
        """
        region = get_region()
        iam = boto3.client("iam", region_name=region)

        role_name = re.match(ROLE_ARN_REGEX, self.arn).group(1)
        managedPolicy = iam.list_attached_role_policies(RoleName=role_name)
        for each in managedPolicy["AttachedPolicies"]:
            iam.detach_role_policy(RoleName=role_name, PolicyArn=each["PolicyArn"])

        inlinePolicy = iam.list_role_policies(RoleName=role_name)
        for each in inlinePolicy["PolicyNames"]:
            iam.delete_role_policy(RoleName=role_name, PolicyName=each)

        instanceProfiles = iam.list_instance_profiles_for_role(RoleName=role_name)
        for each in instanceProfiles["InstanceProfiles"]:
            iam.remove_role_from_instance_profile(
                RoleName=role_name, InstanceProfileName=each["InstanceProfileName"]
            )
        iam.delete_role(RoleName=role_name)

        logging.info(f"Deleted role {role_name}")