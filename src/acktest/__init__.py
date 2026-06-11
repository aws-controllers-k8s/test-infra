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

# Keep the default boto3 session in sync with credentials that the test
# harness rotates on disk during long-running e2e suites. This is a guarded
# no-op outside of that scenario (see acktest.aws.credentials for details).
try:
    from .aws.credentials import install_refreshing_shared_credentials

    install_refreshing_shared_credentials()
except Exception:  # pragma: no cover - never block test imports on this
    import logging

    logging.getLogger(__name__).warning(
        "acktest: could not install rotating credential provider; "
        "falling back to default boto3 credential behavior",
        exc_info=True,
    )