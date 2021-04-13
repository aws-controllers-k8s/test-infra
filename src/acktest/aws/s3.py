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
"""Supports a number of common S3 tasks.
"""

import boto3

from . import identity


def duplicate_bucket_contents(source_bucket: object, destination_bucket: object):
    """ Recursively copies each of the objects in the source bucket into the destination bucket.
    """
    for source_object in source_bucket.objects.all():
        destination_bucket.copy({
            "Bucket": source_object.bucket_name,
            "Key": source_object.key,
        }, source_object.key)

def copy_object(bucket_name: str, copy_source: object, key: str):
    """ Copy an S3 object. Check the following API documentation for input format of the arguments

    https://boto3.amazonaws.com/v1/documentation/api/latest/reference/services/s3.html#S3.Bucket.copy
    """
    region = identity.get_region()
    bucket = boto3.resource("s3", region_name=region).Bucket(bucket_name)
    bucket.copy(copy_source, key)

def delete_object(bucket_name: str, key: str):
    """ Delete an S3 object. Check the following API documentation for input format of the arguments

    https://boto3.amazonaws.com/v1/documentation/api/latest/reference/services/s3.html#S3.Client.delete_objects
    """
    region = identity.get_region()
    bucket = boto3.resource("s3", region_name=region).Bucket(bucket_name)
    bucket.delete_objects(
        Delete={
            "Objects": [
                {
                    "Key": key,
                },
            ],
            "Quiet": False,
        },
    )
