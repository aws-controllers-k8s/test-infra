import boto3

from dataclasses import dataclass, field

from . import Bootstrappable


@dataclass
class Key(Bootstrappable):
    # Outputs
    id: str = field(init=False)

    @property
    def kms_client(self):
        return boto3.client("kms", region_name=self.region)

    def bootstrap(self):
        """Creates a key."""
        key = self.kms_client.create_key()
        self.id = key["KeyMetadata"]["KeyId"]

    def cleanup(self):
        """Disables a key and schedules it for deletion."""
        self.kms_client.disable_key(KeyId=self.id)
        self.kms_client.schedule_key_deletion(KeyId=self.id, PendingWindowInDays=7)
