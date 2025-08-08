import boto3
import json
from dataclasses import dataclass, field

from . import Bootstrappable
from .iam import Role, UserPolicies
from .s3 import Bucket
from .. import resources

@dataclass
class DeliveryStream(Bootstrappable):
    # Inputs
    name_prefix: str
    s3_bucket_prefix: str = "cloudwatch-metric-stream"

    # Subresources
    s3_bucket: Bucket = field(default=None)
    firehose_role: Role = field(default=None)

    # Outputs
    name: str = field(init=False)
    arn: str = field(init=False)

    def __post_init__(self):
        self.name = resources.random_suffix_name(self.name_prefix, 63)
        
        self.s3_bucket = Bucket(
            name_prefix=self.s3_bucket_prefix
        )
        
        # Create IAM role with trust policy for Firehose
        firehose_policy_doc = {
            "Version": "2012-10-17",
            "Statement": [
                {
                    "Effect": "Allow",
                    "Action": [
                        "s3:AbortMultipartUpload",
                        "s3:GetBucketLocation",
                        "s3:GetObject",
                        "s3:ListBucket",
                        "s3:ListBucketMultipartUploads",
                        "s3:PutObject"
                    ],
                    "Resource": [
                        f"arn:aws:s3:::{self.s3_bucket.name}",
                        f"arn:aws:s3:::{self.s3_bucket.name}/*"
                    ]
                }
            ]
        }
        
        self.firehose_role = Role(
            name_prefix="firehose-delivery-role",
            principal_service="firehose.amazonaws.com",
            description="Role for Kinesis Data Firehose delivery stream",
            user_policies=UserPolicies(
                name_prefix="firehose-s3-policy",
                policy_documents=[json.dumps(firehose_policy_doc)]
            )
        )

    @property
    def firehose_client(self):
        return boto3.client("firehose", region_name=self.region)

    def bootstrap(self):
        """Creates a Kinesis Data Firehose delivery stream with S3 destination.
        """
        super().bootstrap()
        
        # Create the delivery stream
        response = self.firehose_client.create_delivery_stream(
            DeliveryStreamName=self.name,
            S3DestinationConfiguration={
                'RoleARN': self.firehose_role.arn,
                'BucketARN': f"arn:aws:s3:::{self.s3_bucket.name}"
            }
        )
        
        self.arn = response['DeliveryStreamARN']

    def cleanup(self):
        """Deletes the Kinesis Data Firehose delivery stream.
        """
        try:
            self.firehose_client.delete_delivery_stream(
                DeliveryStreamName=self.name,
                AllowForceDelete=True
            )
        except Exception:
            pass
        
        super().cleanup()