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
"""Split the reply writer's output into a summary and per-item replies.

The address-feedback reply writer outputs the summary comment first, then
one reply per feedback item, each under a line like `=== F2 ===`. Other
workflows' writers never emit those lines, so their output is all summary.
"""

from __future__ import annotations

import re

# One feedback item's reply header. The ID matches the F1, F2, ... IDs that
# workflows/feedback.py assigns.
REPLY_HEADER_RE = re.compile(r"^=== (F\d+) ===[ \t]*$", re.MULTILINE)


def split_replies(text: str) -> tuple[str, dict[str, str]]:
    """Return (summary, {item ID: reply}).

    The summary is the text before the first reply header. A header with no
    text under it is dropped, and a repeated ID keeps its first reply.
    """
    headers = list(REPLY_HEADER_RE.finditer(text))
    if not headers:
        return text.strip(), {}
    replies: dict[str, str] = {}
    for i, header in enumerate(headers):
        end = headers[i + 1].start() if i + 1 < len(headers) else len(text)
        reply = text[header.end():end].strip()
        if reply and header.group(1) not in replies:
            replies[header.group(1)] = reply
    return text[: headers[0].start()].strip(), replies
