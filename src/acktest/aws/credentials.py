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
"""Keeps the default boto3 session in sync with rotated shared credentials.

Long-running e2e suites (e.g. SageMaker Studio) run for well over an hour. In
Prow the test container is given *static* temporary credentials (a profile with
``aws_access_key_id``/``aws_secret_access_key``/``aws_session_token``) that are
only valid for one hour. The host rotates those credentials in place every 50
minutes, but botocore reads a static-key profile exactly once and caches the
result for the lifetime of the process, so the rotated values are never picked
up. Once the original credentials expire the tests fail with
``ExpiredTokenException``.

``install_refreshing_shared_credentials`` swaps the cached credentials on the
default boto3 session for a ``RefreshableCredentials`` instance that re-reads the
shared credentials file periodically, so every ``boto3.client(...)`` created by
the tests transparently follows the rotation.

This is a no-op (and safe) when:
  * the shared credentials file does not exist,
  * the active profile is an assume-role / web-identity profile (botocore
    already refreshes those), or
  * the ``ACKTEST_DISABLE_CREDENTIAL_REFRESH`` environment variable is set.
"""

import configparser
import datetime
import logging
import os
import threading

import boto3
import botocore.session
from botocore.credentials import RefreshableCredentials

# How long, in seconds, each set of file-sourced credentials is considered
# valid before we re-read the file. botocore refreshes ~15 minutes before this
# expiry, which yields a re-read cadence of roughly (TTL - 900s). With the
# default below that is every 5 minutes, comfortably faster than the 50-minute
# host rotation and the 60-minute credential lifetime.
_DEFAULT_REFRESH_TTL_SECONDS = 20 * 60

_DISABLE_ENV_VAR = "ACKTEST_DISABLE_CREDENTIAL_REFRESH"

# Guards against installing the provider more than once per process.
_install_lock = threading.Lock()
_installed = False


def _shared_credentials_path() -> str:
    return os.environ.get(
        "AWS_SHARED_CREDENTIALS_FILE",
        os.path.expanduser(os.path.join("~", ".aws", "credentials")),
    )


def _active_profile() -> str:
    return os.environ.get(
        "AWS_PROFILE", os.environ.get("AWS_DEFAULT_PROFILE", "default")
    )


def _read_static_profile(creds_file: str, profile: str) -> dict:
    """Returns the static keys for ``profile`` or an empty dict if absent."""
    parser = configparser.ConfigParser()
    parser.read(creds_file)
    if not parser.has_section(profile):
        return {}
    section = parser[profile]
    if "aws_access_key_id" not in section or "aws_secret_access_key" not in section:
        return {}
    return {
        "access_key": section["aws_access_key_id"],
        "secret_key": section["aws_secret_access_key"],
        "token": section.get("aws_session_token"),
    }


def install_refreshing_shared_credentials(
    refresh_ttl_seconds: int = _DEFAULT_REFRESH_TTL_SECONDS,
) -> bool:
    """Make the default boto3 session re-read rotated shared credentials.

    Returns ``True`` if the refreshing provider was installed, ``False`` if the
    current environment does not need it (in which case the default boto3
    behavior is left untouched).
    """
    global _installed

    if os.environ.get(_DISABLE_ENV_VAR):
        return False

    with _install_lock:
        if _installed:
            return True

        creds_file = _shared_credentials_path()
        profile = _active_profile()

        if not os.path.isfile(creds_file):
            return False

        initial = _read_static_profile(creds_file, profile)
        if not initial:
            # Either the profile is missing or it is an assume-role /
            # web-identity profile, both of which botocore already refreshes.
            return False

        def _build_metadata() -> dict:
            keys = _read_static_profile(creds_file, profile)
            if not keys:
                # A rotation may briefly truncate the file. Retry soon rather
                # than crash so we pick up the new values on the next read.
                raise RuntimeError(
                    f"could not read static credentials for profile "
                    f"'{profile}' from '{creds_file}'"
                )
            expiry = datetime.datetime.now(
                datetime.timezone.utc
            ) + datetime.timedelta(seconds=refresh_ttl_seconds)
            keys["expiry_time"] = expiry.isoformat()
            return keys

        credentials = RefreshableCredentials.create_from_metadata(
            metadata=_build_metadata(),
            refresh_using=_build_metadata,
            method="acktest-rotating-shared-credentials",
        )

        botocore_session = botocore.session.Session(profile=profile)
        botocore_session._credentials = credentials

        region = (
            botocore_session.get_config_variable("region")
            or os.environ.get("AWS_DEFAULT_REGION")
            or os.environ.get("AWS_REGION")
        )
        boto3.setup_default_session(
            botocore_session=botocore_session, region_name=region
        )

        _installed = True
        logging.info(
            "acktest: installed rotating credential provider for profile "
            "'%s' (re-reading '%s' every ~%ds)",
            profile,
            creds_file,
            refresh_ttl_seconds,
        )
        return True
