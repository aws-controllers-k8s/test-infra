import boto3
import logging
import json
import re
import time

from dataclasses import dataclass, field
from typing import List

from . import Bootstrappable
from .. import resources

# Regex to match the role name from a role ARN
ROLE_ARN_REGEX = r"^arn:aws:iam::\d{12}:(?:root|user|role\/([A-Za-z0-9-]+))$"

# Time to wait (in seconds) after a role is created.
# Sometimes role propagation takes few seconds, specially
# ServiceLinkedRoles. Waiting after role creation reduces
# the chances of tests getting affected by propagation delay
ROLE_CREATE_WAIT_IN_SECONDS = 30

# Time to wait (in seconds) after a role is deleted
ROLE_DELETE_WAIT_IN_SECONDS = 3

@dataclass
class UserPolicies(Bootstrappable):
    # Inputs
    name_prefix: str
    policy_documents: List[str]

    # Outputs
    names: List[str] = field(init=False, default_factory=lambda: [])
    arns: List[str] = field(init=False, default_factory=lambda: [])

    @property
    def iam_client(self):
        return boto3.client("iam", region_name=self.region)

    def bootstrap(self):
        """Creates a number of IAM policies with auto-generated names.
        """
        super().bootstrap()

        for policy_document in self.policy_documents:
            policy_name = resources.random_suffix_name(self.name_prefix, 64)
            policy = self.iam_client.create_policy(PolicyName=policy_name, PolicyDocument=policy_document)

            self.names.append(policy["Policy"]["PolicyName"])
            self.arns.append(policy["Policy"]["Arn"])

    def cleanup(self):
        """Deletes all created IAM policies.
        """
        super().cleanup()

        for arn in self.arns:
            self.iam_client.delete_policy(PolicyArn=arn)

@dataclass
class Role(Bootstrappable):
    # Inputs
    name_prefix: str
    principal_service: str
    description: str = ""
    managed_policies: List[str] = field(default_factory=lambda: [])

    # Subresources
    user_policies: UserPolicies = field(default=None)

    # Outputs
    name: str = field(init=False)
    arn: str = field(default="", init=False)

    def __post_init__(self):
        self.name = resources.random_suffix_name(self.name_prefix, 63)

    @property
    def iam_client(self):
        return boto3.client("iam", region_name=self.region)

    def bootstrap(self):
        """Creates an IAM role with an auto-generated name.
        """
        super().bootstrap()

        self.iam_client.create_role(
            RoleName=self.name,
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

        for policy in self.managed_policies:
            self.iam_client.attach_role_policy(
                RoleName=self.name,
                PolicyArn=policy,
            )

        if self.user_policies is not None:
            for arn in self.user_policies.arns:
                self.iam_client.attach_role_policy(
                    RoleName=self.name,
                    PolicyArn=arn
                )

        iam_resource = self.iam_client.get_role(RoleName=self.name)
        resource_arn = iam_resource["Role"]["Arn"]

        # There appears to be a delay in role availability after role creation
        # resulting in failure that role is not present. So adding a delay
        # to allow for the role to become available
        time.sleep(ROLE_CREATE_WAIT_IN_SECONDS)

        self.arn = resource_arn

    def cleanup(self):
        """Deletes an IAM role.
        """
        if self.arn:
            managed_policy = self.iam_client.list_attached_role_policies(RoleName=self.name)
            for each in managed_policy["AttachedPolicies"]:
                self.iam_client.detach_role_policy(RoleName=self.name, PolicyArn=each["PolicyArn"])

            inline_policy = self.iam_client.list_role_policies(RoleName=self.name)
            for each in inline_policy["PolicyNames"]:
                self.iam_client.delete_role_policy(RoleName=self.name, PolicyName=each)

            instance_profiles = self.iam_client.list_instance_profiles_for_role(RoleName=self.name)
            for each in instance_profiles["InstanceProfiles"]:
                self.iam_client.remove_role_from_instance_profile(
                    RoleName=self.name, InstanceProfileName=each["InstanceProfileName"]
                )
            self.iam_client.delete_role(RoleName=self.name)

            time.sleep(ROLE_DELETE_WAIT_IN_SECONDS)

        # Policies need to be deleted after they have been detached
        super().cleanup()

@dataclass
class ServiceLinkedRole(Bootstrappable):
    # Inputs
    aws_service_name: str
    default_name: str
    description: str = ""

    # Outputs
    role_name: str = field(default="", init=False)

    @property
    def iam_client(self):
        return boto3.client("iam", region_name=self.region)

    def bootstrap(self):
        """Creates a service-linked role.
        """
        try:
            resp = self.iam_client.create_service_linked_role(
                AWSServiceName=self.aws_service_name,
                Description=self.description
            )
        except self.iam_client.exceptions.InvalidInputException as e:
            # Existance check for SLRs
            if "taken in this account" in str(e):
                logging.info(f"Service-linked role ({self.default_name}) already exists")

                self.role_name = self.default_name
                return
            raise e

        # There appears to be a delay in role availability after role creation
        # resulting in failure that role is not present. So adding a delay
        # to allow for the role to become available
        time.sleep(ROLE_CREATE_WAIT_IN_SECONDS)

        self.role_name = resp["Role"]["RoleName"]

    def cleanup(self):
        """Deletes a service-linked role.
        """
        self.iam_client.delete_service_linked_role(RoleName=self.role_name)
