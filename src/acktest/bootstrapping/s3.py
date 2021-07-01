import boto3
import logging

from dataclasses import dataclass, field

from . import Bootstrappable
from .. import resources
from ..aws.identity import get_region

@dataclass
class Bucket(Bootstrappable):
    # Inputs
    name_prefix: str

    # Outputs
    name: str = field(init=False)
    
    @property
    def s3_client(self):
        return boto3.client("s3", region_name=self.region)

    @property
    def s3_resource(self):
        return boto3.resource("s3", region_name=self.region)

    def bootstrap(self):
        """Creates an S3 bucket with an auto-generated name.
        """
        self.name = resources.random_suffix_name(self.name_prefix, 63)

        if self.region == "us-east-1":
            self.s3_client.create_bucket(Bucket=self.name)
        else:
            self.s3_client.create_bucket(
                Bucket=self.name, CreateBucketConfiguration={"LocationConstraint": self.region}
            )

    def cleanup(self):
        """Deletes an S3 bucket.
        """
        bucket = self.s3_resource.Bucket(self.name)
        bucket.objects.all().delete()
        bucket.delete()