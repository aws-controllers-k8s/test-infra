# Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
#
# Licensed under the Apache License, Version 2.0 (the "License"). You may not use this file except in compliance
# with the License. A copy of the License is located at
#
#    http://www.apache.org/licenses/LICENSE-2.0
#
# or in the 'license' file accompanying this file. This file is distributed on an 'AS IS' BASIS, WITHOUT WARRANTIES
# OR CONDITIONS OF ANY KIND, either express or implied. See the License for the specific language governing permissions
# and limitations under the License.
"""Validation for untrusted workflow invocation values.

This module intentionally uses only the Python standard library so prow-job.sh
can invoke it before using service, resource, or field values in repository
paths and Git commands.
"""

from __future__ import annotations

import argparse
import re

_SERVICE_RE = re.compile(r"^[a-z0-9][a-z0-9-]*$")
_GO_IDENTIFIER_RE = re.compile(r"^[A-Za-z][A-Za-z0-9]*$")
_FIELD_PATH_RE = re.compile(r"^[A-Za-z][A-Za-z0-9]*(?:\.[A-Za-z][A-Za-z0-9]*)*$")


def validate_inputs(
    *,
    service: str,
    resource: str,
    field: str | None = None,
    require_field: bool = False,
) -> list[str]:
    """Return validation problems for one workflow invocation.

    A field can be either one SDK/CRD identifier or a dotted path of identifiers,
    such as ``targetConfiguration.mcp.connector``. Empty path segments and all
    other punctuation are rejected.
    """
    problems: list[str] = []
    if not _SERVICE_RE.fullmatch(service):
        problems.append("service must match ^[a-z0-9][a-z0-9-]*$")
    if not _GO_IDENTIFIER_RE.fullmatch(resource):
        problems.append("resource must be an alphanumeric Go-style identifier")
    if require_field and field is None:
        problems.append("field is required for add-field")
    if field is not None and not _FIELD_PATH_RE.fullmatch(field):
        problems.append(
            "field must be an identifier or dotted path of identifiers "
            "(for example targetConfiguration.mcp.connector)"
        )
    return problems


def main() -> int:
    parser = argparse.ArgumentParser(description="Validate ACK workflow inputs")
    parser.add_argument("--service", required=True)
    parser.add_argument("--resource", required=True)
    parser.add_argument("--field")
    args = parser.parse_args()

    problems = validate_inputs(
        service=args.service,
        resource=args.resource,
        field=args.field,
    )
    if problems:
        parser.error("; ".join(problems))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
