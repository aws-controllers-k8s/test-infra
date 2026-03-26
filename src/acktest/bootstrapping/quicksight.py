# Copyright Amazon.com Inc. or its affiliates. All Rights Reserved.
#
# Licensed under the Apache License, Version 2.0 (the "License"). You may
# not use this file except in compliance with the License. A copy of the
# License is located at
#
# 	 http://aws.amazon.com/apache2.0/
#
# or in the "license" file accompanying this file. This file is distributed
# on an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either
# express or implied. See the License for the specific language governing
# permissions and limitations under the License.

"""QuickSight bootstrapping utilities for e2e tests."""

import boto3
import datetime
import logging
import time

from dataclasses import dataclass, field

from . import Bootstrappable
from .s3 import Bucket

# Wait configuration for subscription activation
DEFAULT_WAIT_TIMEOUT_SECONDS = 300
DEFAULT_WAIT_INTERVAL_SECONDS = 10

# Known subscription status values
ACTIVE_STATUSES = ['ACCOUNT_CREATED', 'OK']
IN_PROGRESS_STATUSES = ['SIGNUP_ATTEMPT_IN_PROGRESS']
ERROR_STATUSES = ['CREATION_FAILED', 'UNSUBSCRIBED']


@dataclass
class Subscription(Bootstrappable):
    """Ensures a QuickSight subscription exists for the AWS account.

    Creates the subscription if it doesn't exist and waits until it reaches
    an active state. Does NOT delete the subscription on cleanup since
    QuickSight subscriptions are account-level and typically persist.
    """

    # Inputs
    notification_email: str = "ack-infra+quicksight-resources@amazon.com"
    edition: str = "ENTERPRISE"
    wait_timeout_seconds: int = DEFAULT_WAIT_TIMEOUT_SECONDS
    wait_interval_seconds: int = DEFAULT_WAIT_INTERVAL_SECONDS

    # Outputs
    account_id: str = field(init=False, default="")
    subscription_status: str = field(init=False, default="")
    subscription_edition: str = field(init=False, default="")

    @property
    def quicksight_client(self):
        return boto3.client("quicksight", region_name=self.region)

    def _get_subscription_status(self) -> dict:
        """Returns the QuickSight subscription info, or None if not found."""
        from botocore.exceptions import ClientError
        try:
            resp = self.quicksight_client.describe_account_subscription(
                AwsAccountId=self.account_id
            )
            return resp.get('AccountInfo')
        except ClientError as e:
            if e.response['Error']['Code'] == 'ResourceNotFoundException':
                return None
            raise

    def _create_subscription(self):
        """Creates a QuickSight subscription for the account."""
        from botocore.exceptions import ClientError
        try:
            self.quicksight_client.create_account_subscription(
                AwsAccountId=self.account_id,
                AccountName=f'quicksight-test-{self.account_id}',
                NotificationEmail=self.notification_email,
                Edition=self.edition,
                AuthenticationMethod='IAM_AND_QUICKSIGHT'
            )
            logging.info(f"Created QuickSight subscription for account {self.account_id}")
        except ClientError as e:
            if e.response['Error']['Code'] == 'ResourceExistsException':
                logging.info(f"QuickSight subscription already exists for account {self.account_id}")
            else:
                raise

    def _wait_until_active(self) -> dict:
        """Waits until the QuickSight subscription is in an active state."""
        logging.info(f"Waiting for QuickSight subscription to become active (timeout: {self.wait_timeout_seconds}s)...")

        start_time = datetime.datetime.now()
        timeout = start_time + datetime.timedelta(seconds=self.wait_timeout_seconds)
        status = None

        while datetime.datetime.now() < timeout:
            status = self._get_subscription_status()

            if status is None:
                logging.warning("Subscription not found, waiting...")
                time.sleep(self.wait_interval_seconds)
                continue

            current = status.get('AccountSubscriptionStatus', 'UNKNOWN')
            logging.info(f"Current subscription status: {current}")

            if current in ACTIVE_STATUSES:
                elapsed = (datetime.datetime.now() - start_time).total_seconds()
                logging.info(f"QuickSight subscription is ready (status: {current}, took {elapsed:.1f}s)")
                return status

            if current in ERROR_STATUSES:
                raise RuntimeError(
                    f"QuickSight subscription entered error state: {current}. "
                    f"Check AWS console for details."
                )

            time.sleep(self.wait_interval_seconds)

        elapsed = (datetime.datetime.now() - start_time).total_seconds()
        current = status.get('AccountSubscriptionStatus', 'UNKNOWN') if status else 'NOT_FOUND'
        raise TimeoutError(
            f"Timed out waiting for QuickSight subscription after {elapsed:.1f}s. "
            f"Current status: {current}. Expected one of: {ACTIVE_STATUSES}"
        )

    def bootstrap(self):
        """Ensures QuickSight subscription exists and is active."""
        super().bootstrap()

        self.account_id = str(self.account_id) if self.account_id else str(super().account_id)

        status = self._get_subscription_status()
        if status is None:
            logging.info(f"Creating QuickSight subscription for account {self.account_id}")
            self._create_subscription()

        info = self._wait_until_active()
        self.subscription_status = info.get('AccountSubscriptionStatus', '')
        self.subscription_edition = info.get('Edition', self.edition)
        logging.info(f"QuickSight subscription ready: edition={self.subscription_edition}")

    def cleanup(self):
        """No-op: QuickSight subscriptions are account-level and persist."""
        super().cleanup()
        logging.info("Skipping QuickSight subscription cleanup (account-level resource)")


@dataclass
class S3DataSource(Bootstrappable):
    """Creates an S3 bucket with sample CSV data and manifest for QuickSight
    DataSource testing.

    Wraps an S3 Bucket bootstrappable and uploads sample data after creation.
    """

    # Inputs
    name_prefix: str = "qs-test-bucket"
    data_file_key: str = "data.csv"
    manifest_key: str = "manifest.json"

    # Subresources (auto-bootstrapped by parent)
    bucket: Bucket = field(init=False, default=None)

    # Outputs
    bucket_name: str = field(init=False, default="")

    def __post_init__(self):
        self.bucket = Bucket(name_prefix=self.name_prefix)

    @property
    def s3_client(self):
        return boto3.client("s3", region_name=self.region)

    def bootstrap(self):
        """Creates the S3 bucket and uploads sample data."""
        super().bootstrap()

        self.bucket_name = self.bucket.name

        csv_content = (
            "id,name,value,category\n"
            "1,Item A,100,electronics\n"
            "2,Item B,200,books\n"
            "3,Item C,150,electronics\n"
            "4,Item D,300,clothing\n"
            "5,Item E,250,books\n"
        )
        self.s3_client.put_object(
            Bucket=self.bucket_name, Key=self.data_file_key, Body=csv_content
        )

        manifest_content = (
            '{"fileLocations": [{"URIs": '
            f'["s3://{self.bucket_name}/{self.data_file_key}"]'
            '}], "globalUploadSettings": {"format": "CSV", '
            '"containsHeader": true, "delimiter": ","}}'
        )
        self.s3_client.put_object(
            Bucket=self.bucket_name, Key=self.manifest_key, Body=manifest_content
        )

        logging.info(f"Uploaded sample data to s3://{self.bucket_name}/{self.data_file_key}")
        logging.info(f"Uploaded manifest to s3://{self.bucket_name}/{self.manifest_key}")

    def cleanup(self):
        """Delegates to the underlying Bucket cleanup."""
        super().cleanup()
