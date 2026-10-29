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
"""Post the agent's per-item replies to pull request review threads.

prow-job.sh runs this after the address-feedback workflow, once the push has
succeeded or the run made no code change, and before it posts the summary
comment. Two files come in:

- the targets file, written by workflows/feedback.py before the agents ran,
  mapping review thread items (F2, ...) to thread node IDs;
- the replies file, written by the harness from the reply writer's output,
  mapping items to reply text.

Only items in both get a reply, so the agents choose what to say but never
which comments to answer. Items without a thread (review summaries and
conversation comments) are covered by the summary comment instead.

Usage:
    python workflows/reply.py --targets FILE --replies FILE

Prints the number of replies posted. A reply that fails is reported on
stderr and does not stop the others or fail the job.
"""

from __future__ import annotations

import argparse
import json
import re
import subprocess
import sys
from pathlib import Path
from typing import Any, Callable

MUTATION = """
mutation($threadId: ID!, $body: String!) {
  addPullRequestReviewThreadReply(input: {pullRequestReviewThreadId: $threadId, body: $body}) {
    comment { url }
  }
}
"""

# GitHub allows 65536 characters; a reply this long means the writer
# misbehaved, so cut it well before that.
MAX_REPLY_CHARS = 4000

_ITEM_ID_RE = re.compile(r"^F(\d+)$")


def load_map(path: str) -> dict[str, str]:
    """Read a JSON object of string to string. Anything else reads as empty."""
    try:
        data = json.loads(Path(path).read_text())
    except (OSError, ValueError) as exc:
        print(f"reply.py: cannot read {path}: {exc}", file=sys.stderr)
        return {}
    if not isinstance(data, dict):
        return {}
    return {k: v for k, v in data.items() if isinstance(k, str) and isinstance(v, str)}


def plan_replies(targets: dict[str, str], replies: dict[str, str]) -> list[tuple[str, str, str]]:
    """(item ID, thread ID, body) for every item with both a target and a reply, in item order."""
    planned = []
    items = sorted((int(m.group(1)), k) for k in targets if (m := _ITEM_ID_RE.match(k)))
    for _, item_id in items:
        body = replies.get(item_id, "").strip()
        if not body or not targets[item_id]:
            continue
        if len(body) > MAX_REPLY_CHARS:
            body = body[: MAX_REPLY_CHARS - 1].rstrip() + "…"
        planned.append((item_id, targets[item_id], body))
    return planned


def post_reply(thread_id: str, body: str, run: Callable[..., Any] = subprocess.run) -> str:
    """Reply in one review thread with gh. Returns the new comment's URL."""
    # -f passes values as raw strings; -F would read a body starting with
    # "@" as a file path.
    proc = run(
        [
            "gh", "api", "graphql",
            "-f", f"query={MUTATION}",
            "-f", f"threadId={thread_id}",
            "-f", f"body={body}",
        ],
        capture_output=True,
        text=True,
        check=True,
        timeout=60,
    )
    data = json.loads(proc.stdout)
    if data.get("errors"):
        raise RuntimeError(data["errors"])
    return data["data"]["addPullRequestReviewThreadReply"]["comment"]["url"]


def main(argv: list[str] | None = None, run: Callable[..., Any] = subprocess.run) -> int:
    parser = argparse.ArgumentParser(description="Reply in PR review threads")
    parser.add_argument("--targets", required=True)
    parser.add_argument("--replies", required=True)
    args = parser.parse_args(argv)

    posted = 0
    for item_id, thread_id, body in plan_replies(load_map(args.targets), load_map(args.replies)):
        try:
            url = post_reply(thread_id, body, run)
        except (subprocess.SubprocessError, OSError, ValueError, KeyError, TypeError, RuntimeError) as exc:
            print(f"reply.py: could not reply for {item_id}: {exc}", file=sys.stderr)
            continue
        print(f"reply.py: replied for {item_id}: {url}", file=sys.stderr)
        posted += 1
    print(posted)
    return 0


if __name__ == "__main__":
    sys.exit(main())
