import boto3

from dataclasses import dataclass, field
from typing import Union

from .. import resources
from . import Bootstrappable
from .kms import Key


@dataclass
class Secret(Bootstrappable):
    # Inputs
    name_prefix: Union[str, None] = field(default=None)
    plain_text: str = field(default='{" ":" "}')

    # Subresources
    # There is no charge for customer managed KMS keys that are scheduled for deletion.
    # This is done in super.cleanup().
    kms_key: Key = field(init=False, default=None)

    # Outputs
    name: str = field(init=False)
    arn: str = field(default="", init=False)

    def __post_init__(self):
        self.name = resources.random_suffix_name(self.name_prefix, 63)
        self.kms_key = Key()

    @property
    def secretsmanager_client(self):
        return boto3.client("secretsmanager", region_name=self.region)

    def bootstrap(self):
        """Creates a secret and all subresources."""
        super().bootstrap()
        secret = self.secretsmanager_client.create_secret(
            Name=self.name, KmsKeyId=self.kms_key.id, SecretString=self.plain_text
        )
        self.arn = secret["ARN"]
        print(f"Created secret with ARN: {self.arn}")

    def cleanup(self):
        """Schedules a secret for deletion and all subresources."""
        self.secretsmanager_client.delete_secret(
            SecretId=self.arn, RecoveryWindowInDays=7
        )
        print(f"Scheduled secret with ARN {self.arn} for deletion")
        super().cleanup()
