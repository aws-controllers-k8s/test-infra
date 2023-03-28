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
class Queue(Bootstrappable):
    # Inputs
    name_prefix: str
    policy: str = ""
    policy_vars: dict = field(default_factory=dict)

    # Outputs
    name: str = field(init=False)
    arn: str = field(init=False)

    def __post_init__(self):
        self.name = resources.random_suffix_name(self.name_prefix, 63)

    @property
    def sqs_client(self):
        return boto3.client("sqs", region_name=self.region)

    @property
    def sqs_resource(self):
        return boto3.resource("sqs", region_name=self.region)

    def bootstrap(self):
        """Creates an SQS queue with an auto-generated name.
        """
        create_attributes = {}

        if self.policy != "":
            self.policy_vars.update({
                "$NAME": self.name,
                "$ACCOUNT_ID": self.account_id,
                "$REGION": self.region,
            })

            for key, value in self.policy_vars.items():
                self.policy = self.policy.replace(key, value)

            create_attributes["Policy"] = self.policy

        queue = self.sqs_resource.create_queue(
            QueueName=self.name,
            Attributes=create_attributes,
        )
        self.url = queue.url
        self.arn = queue.attributes["QueueArn"]

    def cleanup(self):
        """Deletes an SQS queue.
        """
        self.sqs_client.delete_queue(
            QueueUrl=self.url,
        )
