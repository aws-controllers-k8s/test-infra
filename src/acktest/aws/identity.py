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
"""Supports a number of common AWS STS and IAM tasks.
"""

import boto3


def get_account_id() -> int:
    return boto3.client('sts').get_caller_identity().get('Account')


def get_region(default: str = "us-west-2") -> str:
    return boto3.session.Session().region_name or default

def get_partition() -> str:
    return boto3.client('sts').get_caller_identity().get('Arn').split(':')[1]