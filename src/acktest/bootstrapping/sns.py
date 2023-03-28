import boto3

from dataclasses import dataclass, field

from . import Bootstrappable
from .. import resources

@dataclass
class Topic(Bootstrappable):
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
    def sns_client(self):
        return boto3.client("sns", region_name=self.region)

    @property
    def sns_resource(self):
        return boto3.resource("sns", region_name=self.region)

    def bootstrap(self):
        """Creates an SNS topic with an auto-generated name.
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

        topic = self.sns_client.create_topic(
            Name=self.name,
            Attributes=create_attributes
        )
        self.arn = topic["TopicArn"]

    def cleanup(self):
        """Deletes an SNS topic.
        """
        self.sns_client.delete_topic(TopicArn=self.arn)
