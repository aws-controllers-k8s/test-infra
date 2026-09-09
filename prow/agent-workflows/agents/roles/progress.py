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
"""Node-aware progress rendering for the add-resource graph.

Consumes the structured events from `Graph.stream_async()` and prints tagged,
human-readable progress so it is always clear WHICH agent is acting and what it
decided — instead of the undifferentiated text wall produced by the default
blocking call + PrintingCallbackHandler.

Strands graph event contract (verified against strands-agents >= 1.45):
  {"type": "multiagent_node_start",  "node_id": str, "node_type": str}
  {"type": "multiagent_node_stream", "node_id": str, "event": <agent event>}
      where the inner agent event carries incremental text at event["data"]
  {"type": "multiagent_node_stop",   "node_id": str, "node_result": NodeResult}
  {"type": "multiagent_handoff",     ...}                  # node transitions
  {"type": "multiagent_result",      "result": GraphResult} # final result
"""

from __future__ import annotations

import sys
from dataclasses import dataclass, field

from .graph import IMPL_REVIEWER, IMPLEMENTER, PLAN_REVIEWER, PLANNER
from .verdict import Decision, parse_decision

# Friendly labels for the node ids.
_LABELS = {
    PLANNER: "planner",
    PLAN_REVIEWER: "plan-review",
    IMPLEMENTER: "implementer",
    IMPL_REVIEWER: "review",
}
# Nodes whose final output is a verdict we want to surface on completion.
_REVIEWER_NODES = {PLAN_REVIEWER, IMPL_REVIEWER}


def _node_text(node_result) -> str:
    """Extract the latest agent text from a NodeResult (or '' if none)."""
    if node_result is None:
        return ""
    try:
        agent_results = node_result.get_agent_results()
    except Exception:
        return ""
    return str(agent_results[-1]) if agent_results else ""


@dataclass
class ProgressReporter:
    """Stateful renderer for one graph run.

    Tracks per-node execution counts so it can label iterations (e.g.
    "implementer (iteration 2)") and emits a one-line summary per node
    transition. Verbose mode additionally streams the agents' incremental text,
    prefixed with the active node so lines never get attributed to the wrong
    agent.
    """

    verbose: bool = False
    stream: object = field(default=None)  # file-like; defaults to sys.stdout
    _runs: dict = field(default_factory=dict)
    _active: str | None = None
    _wrote_stream_text: bool = False

    def _out(self):
        return self.stream or sys.stdout

    def _w(self, text: str) -> None:
        self._out().write(text)
        self._out().flush()

    def _line(self, text: str) -> None:
        # Ensure node banners start on a fresh line even mid-stream.
        if self._wrote_stream_text:
            self._w("\n")
            self._wrote_stream_text = False
        self._w(text + "\n")

    def handle(self, event: dict) -> None:
        etype = event.get("type")
        if etype == "multiagent_node_start":
            self._on_start(event)
        elif etype == "multiagent_node_stream":
            if self.verbose:
                self._on_stream(event)
        elif etype == "multiagent_node_stop":
            self._on_stop(event)
        # multiagent_handoff / multiagent_result need no live rendering here.

    def _on_start(self, event: dict) -> None:
        node_id = event.get("node_id", "?")
        self._runs[node_id] = self._runs.get(node_id, 0) + 1
        self._active = node_id
        label = _LABELS.get(node_id, node_id)
        n = self._runs[node_id]
        suffix = f" (iteration {n})" if n > 1 else ""
        self._line(f"\n▶ {label}{suffix} started")

    def _on_stream(self, event: dict) -> None:
        inner = event.get("event") or {}
        data = inner.get("data")
        if not data:
            return
        # Prefix the very first chunk of a node's stream so attribution is clear.
        if not self._wrote_stream_text:
            label = _LABELS.get(self._active, self._active or "?")
            self._w(f"    [{label}] ")
        self._w(str(data))
        self._wrote_stream_text = True

    def _on_stop(self, event: dict) -> None:
        node_id = event.get("node_id", "?")
        label = _LABELS.get(node_id, node_id)
        node_result = event.get("node_result")
        status = getattr(getattr(node_result, "status", None), "name", "?")

        verdict_str = ""
        if node_id in _REVIEWER_NODES:
            text = _node_text(node_result)
            if text.strip():
                decision = parse_decision(text)
                verdict_str = f" → {decision.value}"
        self._line(f"✓ {label} completed [{status}]{verdict_str}")


def consume(events, reporter: ProgressReporter):
    """Drive a sync iterator of events through the reporter (for tests)."""
    for ev in events:
        reporter.handle(ev)
