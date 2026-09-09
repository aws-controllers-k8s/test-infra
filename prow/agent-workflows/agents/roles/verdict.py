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
"""Parse the reviewer's APPROVE/REVISE decision from its output text.

The reviewer's output follows roles/schemas/review-output.md, which begins with
a line `## Decision: APPROVE` or `## Decision: REVISE`. We parse defensively
because LLM output formatting drifts: we look for the Decision header first,
then fall back to a conservative scan. When ambiguous we return REVISE — never
silently approve, matching the SOP's "Do NOT approve work that doesn't compile".
"""

from __future__ import annotations

import re
from enum import Enum


class Decision(str, Enum):
    APPROVE = "APPROVE"
    REVISE = "REVISE"


_DECISION_HEADER = re.compile(
    r"^\s{0,3}#{0,4}\s*Decision\s*:?\s*\**\s*(APPROVE|REVISE)\b",
    re.IGNORECASE | re.MULTILINE,
)


def parse_decision(review_text: str) -> Decision:
    """Extract APPROVE/REVISE from a reviewer document.

    Conservative: defaults to REVISE if no clear APPROVE is found.
    """
    if not review_text:
        return Decision.REVISE

    m = _DECISION_HEADER.search(review_text)
    if m:
        return Decision.APPROVE if m.group(1).upper() == "APPROVE" else Decision.REVISE

    # Fallback: only treat as APPROVE if an explicit approve token appears and
    # no explicit revise token appears, to avoid false positives from prose.
    upper = review_text.upper()
    has_approve = bool(re.search(r"\bAPPROVE(D)?\b", upper))
    has_revise = bool(re.search(r"\bREVISE\b", upper))
    if has_approve and not has_revise:
        return Decision.APPROVE
    return Decision.REVISE
