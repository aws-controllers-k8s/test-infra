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
"""Unit tests for acktest.aws.credentials."""

import configparser
import time

import boto3
import pytest

from acktest.aws import credentials


def _write_static_profile(path, profile, token):
    parser = configparser.ConfigParser()
    parser[profile] = {
        "aws_access_key_id": f"AK_{token}",
        "aws_secret_access_key": f"SK_{token}",
        "aws_session_token": token,
    }
    with open(path, "w") as f:
        parser.write(f)


def _write_assume_role_profile(path, profile):
    parser = configparser.ConfigParser()
    parser[profile] = {
        "role_arn": "arn:aws:iam::000000000000:role/example",
        "source_profile": "base",
    }
    with open(path, "w") as f:
        parser.write(f)


@pytest.fixture(autouse=True)
def reset_state(monkeypatch):
    """Reset the one-time install guard and default boto3 session per test."""
    monkeypatch.setattr(credentials, "_installed", False)
    monkeypatch.setattr(boto3, "DEFAULT_SESSION", None)
    monkeypatch.delenv(credentials._DISABLE_ENV_VAR, raising=False)
    monkeypatch.setenv("AWS_DEFAULT_REGION", "us-west-2")
    yield


def test_picks_up_rotated_static_credentials(tmp_path, monkeypatch):
    creds_file = tmp_path / "credentials"
    _write_static_profile(creds_file, "ack-test", "token_v1")
    monkeypatch.setenv("AWS_SHARED_CREDENTIALS_FILE", str(creds_file))
    monkeypatch.setenv("AWS_PROFILE", "ack-test")

    # Tiny TTL so the advisory refresh window triggers a re-read on each access.
    assert credentials.install_refreshing_shared_credentials(refresh_ttl_seconds=1)

    creds = boto3.DEFAULT_SESSION.get_credentials()
    assert creds.method == "acktest-rotating-shared-credentials"
    assert creds.get_frozen_credentials().token == "token_v1"

    # Simulate the host rotating the credentials file in place.
    _write_static_profile(creds_file, "ack-test", "token_v2")
    time.sleep(1.2)

    assert creds.get_frozen_credentials().token == "token_v2"


def test_noop_for_assume_role_profile(tmp_path, monkeypatch):
    creds_file = tmp_path / "credentials"
    _write_assume_role_profile(creds_file, "ack-test")
    monkeypatch.setenv("AWS_SHARED_CREDENTIALS_FILE", str(creds_file))
    monkeypatch.setenv("AWS_PROFILE", "ack-test")

    # Assume-role profiles are already refreshed by botocore; leave them alone.
    assert credentials.install_refreshing_shared_credentials() is False


def test_noop_when_credentials_file_missing(tmp_path, monkeypatch):
    monkeypatch.setenv(
        "AWS_SHARED_CREDENTIALS_FILE", str(tmp_path / "does-not-exist")
    )
    monkeypatch.setenv("AWS_PROFILE", "ack-test")

    assert credentials.install_refreshing_shared_credentials() is False


def test_noop_when_disabled_by_env(tmp_path, monkeypatch):
    creds_file = tmp_path / "credentials"
    _write_static_profile(creds_file, "ack-test", "token_v1")
    monkeypatch.setenv("AWS_SHARED_CREDENTIALS_FILE", str(creds_file))
    monkeypatch.setenv("AWS_PROFILE", "ack-test")
    monkeypatch.setenv(credentials._DISABLE_ENV_VAR, "1")

    assert credentials.install_refreshing_shared_credentials() is False


def test_noop_when_profile_absent(tmp_path, monkeypatch):
    creds_file = tmp_path / "credentials"
    _write_static_profile(creds_file, "some-other-profile", "token_v1")
    monkeypatch.setenv("AWS_SHARED_CREDENTIALS_FILE", str(creds_file))
    monkeypatch.setenv("AWS_PROFILE", "ack-test")

    assert credentials.install_refreshing_shared_credentials() is False
