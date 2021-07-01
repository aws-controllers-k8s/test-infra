import boto3
import logging
import json
import re
import time

from dataclasses import dataclass, field
from typing import List

from . import BootstrappableResource
from .. import resources
from ..aws.identity import get_region, get_account_id

@dataclass
class Bucket(BootstrappableResource):
    # Inputs
    name_prefix: str

    # Outputs
    name: str = field(init=False)
    
    def bootstrap(self):
        """Creates an S3 bucket with an auto-generated name.

        Args:
            name_prefix (str): The prefix for the auto-generated name.
        """
        region = get_region()
        self.name = resources.random_suffix_name(self.name_prefix, 63)

        s3 = boto3.client("s3", region_name=region)
        if region == "us-east-1":
            s3.create_bucket(Bucket=self.name)
        else:
            s3.create_bucket(
                Bucket=self.name, CreateBucketConfiguration={"LocationConstraint": region}
            )

        logging.info(f"Created bucket {self.name}")

    def cleanup(self):
        """Deletes an S3 bucket.
        """
        region = get_region()
        s3_resource = boto3.resource("s3", region_name=region)

        bucket = s3_resource.Bucket(self.name)
        bucket.objects.all().delete()
        bucket.delete()

        logging.info(f"Deleted data bucket {self.name}")