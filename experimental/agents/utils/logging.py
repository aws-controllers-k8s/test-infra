# Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
#
# Licensed under the Apache License, Version 2.0 (the "License"). You may not use this file except in compliance
# with the License. A copy of the License is located at
#
#    http://www.apache.org/licenses/LICENSE-2.0
#
# or in the 'license' file accompanying this file. This file is distributed on an 'AS IS' BASIS, WITHOUT WARRANTIES
# OR CONDITIONS OF ANY KIND, express or implied. See the License for the specific language governing permissions
# and limitations under the License.
"""Utilities for setting up Agent logging."""

import logging
import warnings

def configure_logging(debug=False):
    """Configure logging for the application.

    Args:
        debug: Whether to enable debug logging
    """
    log_level = logging.DEBUG if debug else logging.INFO
    logging.basicConfig(level=log_level, format="%(levelname)s | %(name)s | %(message)s")

    # Always enable Strands debug logs if debug is enabled
    if debug:
        logging.getLogger("strands").setLevel(logging.DEBUG)

    # Suppress deprecation warnings from botocore about datetime.utcnow()
    warnings.filterwarnings("ignore", category=DeprecationWarning, module="botocore")