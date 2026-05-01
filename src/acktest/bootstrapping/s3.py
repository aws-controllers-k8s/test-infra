import boto3
import logging

from dataclasses import dataclass, field
from typing import List

from . import Bootstrappable
from .. import resources

@dataclass
class Bucket(Bootstrappable):
    # Inputs
    name_prefix: str
    enable_versioning: bool = False
    policy: str = ""
    policy_vars: dict = field(default_factory=dict)
    # Optional list of object keys to pre-create as zero-byte objects
    # after the bucket is created.
    empty_objects: List[str] = field(default_factory=list)

    # Outputs
    name: str = field(init=False)

    def __post_init__(self):
        self.name = resources.random_suffix_name(self.name_prefix, 63)

    @property
    def s3_client(self):
        return boto3.client("s3", region_name=self.region)

    @property
    def s3_resource(self):
        return boto3.resource("s3", region_name=self.region)

    def bootstrap(self):
        """Creates an S3 bucket with an auto-generated name.
        """
        if self.region == "us-east-1":
            self.s3_client.create_bucket(Bucket=self.name)
        else:
            self.s3_client.create_bucket(
                Bucket=self.name, CreateBucketConfiguration={"LocationConstraint": self.region}
            )

        if self.enable_versioning:
            self.s3_client.put_bucket_versioning(
                Bucket=self.name,
                VersioningConfiguration={
                    "Status": "Enabled"
                }
            )

        if self.policy != "":
            self.policy_vars.update({
                "$NAME": self.name,
                "$ACCOUNT_ID": self.account_id,
                "$REGION": self.region,
            })

            for key, value in self.policy_vars.items():
                self.policy = self.policy.replace(key, value)

            self.s3_client.put_bucket_policy(
                Bucket=self.name,
                Policy=self.policy,
            )

        for key in self.empty_objects:
            self.s3_client.put_object(Bucket=self.name, Key=key, Body=b"")
            logging.info(f"Created empty object s3://{self.name}/{key}")

    def cleanup(self):
        """Deletes an S3 bucket and its contents.

        When versioning is enabled, `bucket.objects.all().delete()` only
        removes the current versions; non-current versions and delete
        markers are left behind and `DeleteBucket` then fails with
        `BucketNotEmpty`. Purge every version and delete marker before
        deleting the bucket so cleanup works for both versioned and
        non-versioned buckets.
        """
        bucket = self.s3_resource.Bucket(self.name)
        if self.enable_versioning:
            paginator = self.s3_client.get_paginator("list_object_versions")
            for page in paginator.paginate(Bucket=self.name):
                to_delete = []
                for v in page.get("Versions", []) or []:
                    to_delete.append({"Key": v["Key"], "VersionId": v["VersionId"]})
                for m in page.get("DeleteMarkers", []) or []:
                    to_delete.append({"Key": m["Key"], "VersionId": m["VersionId"]})
                # `delete_objects` accepts up to 1000 keys per call; a single
                # page is already capped at that, so one call per page is
                # sufficient.
                if to_delete:
                    self.s3_client.delete_objects(
                        Bucket=self.name,
                        Delete={"Objects": to_delete, "Quiet": True},
                    )
        else:
            bucket.objects.all().delete()
        bucket.delete()
