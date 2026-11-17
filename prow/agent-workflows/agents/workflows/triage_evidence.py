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
"""Deterministic evidence for one ACK Autopilot triage item (mode api, Plan 8).

Everything here is computed by code from two public inputs: the AWS SDK model
JSON (Smithy JSON AST) at the item's SDK version, and the controller
repository at main (generator.yaml and config/crd/bases). Nothing here calls a
model or the network. The same pack checks the names a model's answer cites:
a reference that does not resolve here makes the answer undecided (design
5.1: every operation, shape or field it names must exist in the model or CRD).
"""

from __future__ import annotations

import json
import re
from dataclasses import dataclass, field
from pathlib import Path

import yaml

from workflows.result import TRIAGE_REF_RE as REF_RE

MAX_MODEL_BYTES = 64 << 20
MAX_DOC_CHARS = 1500
MAX_CONFIG_CHARS = 4000
MAX_LISTED = 20
_STRUCTS = ("structure", "union")
_TAG_RE = re.compile(r"<[^>]*>")
_SPACE_RE = re.compile(r"\s+")
_MODEL_NAME_RE = re.compile(r"^[a-z0-9][a-z0-9-]{0,63}$")


def _target(ref: object) -> str | None:
    if isinstance(ref, dict) and isinstance(ref.get("target"), str):
        return ref["target"]
    return None


def _local(shape_id: str) -> str:
    return shape_id.split("#", 1)[-1]


def _cap(text: str, limit: int) -> str:
    return text if len(text) <= limit else text[: limit - 1] + "…"


class Model:
    """The parts of a Smithy JSON AST model triage reads."""

    def __init__(self, doc: object):
        shapes = doc.get("shapes") if isinstance(doc, dict) else None
        if not isinstance(shapes, dict) or not shapes:
            raise ValueError("model has no shapes")
        self.shapes = {k: v for k, v in shapes.items() if isinstance(k, str) and isinstance(v, dict)}
        services = sorted(k for k, v in self.shapes.items() if v.get("type") == "service")
        if len(services) != 1:
            raise ValueError(f"model has {len(services)} service shapes, want 1")
        self.namespace = services[0].split("#", 1)[0]
        self.operations = sorted(
            _local(k) for k, v in self.shapes.items()
            if v.get("type") == "operation" and k.startswith(self.namespace + "#")
        )
        self._operation_set = set(self.operations)
        self._local_names = {_local(k) for k in self.shapes}

    @classmethod
    def load(cls, path: Path) -> "Model":
        if path.stat().st_size > MAX_MODEL_BYTES:
            raise ValueError(f"{path} is larger than {MAX_MODEL_BYTES} bytes")
        return cls(json.loads(path.read_text()))

    def has_operation(self, name: str) -> bool:
        return name in self._operation_set

    def has_shape(self, name: str) -> bool:
        return name in self._local_names

    def type_of(self, shape_id: str) -> str:
        shape = self.shapes.get(shape_id)
        return str(shape.get("type", "unknown")) if shape else "unknown"

    def _step(self, shape_id: str) -> str:
        """Step through lists, sets and maps to the element shape."""
        for _ in range(8):
            shape = self.shapes.get(shape_id)
            if shape is None:
                return shape_id
            kind = shape.get("type")
            if kind in ("list", "set"):
                nxt = _target(shape.get("member"))
            elif kind == "map":
                nxt = _target(shape.get("value"))
            else:
                return shape_id
            if nxt is None:
                return shape_id
            shape_id = nxt
        return shape_id

    def _members(self, shape_id: str) -> dict:
        shape = self.shapes.get(shape_id)
        if shape is None or shape.get("type") not in _STRUCTS:
            return {}
        members = shape.get("members")
        return members if isinstance(members, dict) else {}

    def resolve(self, root: str, path: str) -> tuple[str, dict] | None:
        """The (target shape id, member) of path's last segment, walked from
        root, or None. Lists, sets and maps are stepped through without using
        a segment; a segment matches a member name exactly, else
        case-insensitively (ACK paths are not always cased like the model)."""
        cur, member = root, None
        for segment in path.split("."):
            members = self._members(self._step(cur))
            name = segment if segment in members else next(
                (k for k in sorted(members) if k.lower() == segment.lower()), None)
            if name is None or not isinstance(members[name], dict):
                return None
            member = members[name]
            cur = _target(member)
            if cur is None:
                return None
        return (cur, member) if member is not None else None

    def find_field(self, path: str) -> tuple[str, str, dict] | None:
        """(root, target, member) for the first structure, by shape id, that
        path resolves from. A field exists if any structure in the model has
        it: detector subjects are relative to an operation's input, its
        output, or a resource element inside an output."""
        first = path.split(".", 1)[0].lower()
        for shape_id in sorted(self.shapes):
            if self.shapes[shape_id].get("type") not in _STRUCTS:
                continue
            if not any(k.lower() == first for k in self._members(shape_id)):
                continue
            found = self.resolve(shape_id, path)
            if found:
                return shape_id, found[0], found[1]
        return None

    def _io(self, operation: str, key: str) -> str | None:
        return _target(self.shapes.get(f"{self.namespace}#{operation}", {}).get(key))

    def _near(self, root: str) -> list[str]:
        """root and the structures up to two member hops below it, so a path
        relative to an element of a read's response is found."""
        seen, frontier = [root], [root]
        for _ in range(2):
            nxt = []
            for shape_id in frontier:
                for member in self._members(self._step(shape_id)).values():
                    target = _target(member)
                    if target is None:
                        continue
                    target = self._step(target)
                    if target not in seen and self.shapes.get(target, {}).get("type") in _STRUCTS:
                        seen.append(target)
                        nxt.append(target)
            frontier = nxt
        return seen

    def set_by(self, path: str) -> list[str]:
        out = []
        for op in self.operations:
            root = self._io(op, "input")
            if root and self.resolve(root, path):
                out.append(op)
        return out[:MAX_LISTED]

    def read_by(self, path: str) -> list[str]:
        out = []
        for op in self.operations:
            root = self._io(op, "output")
            if root and any(self.resolve(r, path) for r in self._near(root)):
                out.append(op)
        return out[:MAX_LISTED]

    def resource_operations(self, resource: str) -> list[str]:
        needle = resource.lower()
        return [op for op in self.operations if needle in op.lower()][:MAX_LISTED]


def documentation(member: dict, target: dict | None) -> str:
    """The member's documentation (else its target shape's) as plain text."""
    for source in (member, target or {}):
        traits = source.get("traits") if isinstance(source, dict) else None
        doc = traits.get("smithy.api#documentation") if isinstance(traits, dict) else None
        if isinstance(doc, str) and doc.strip():
            return _cap(_SPACE_RE.sub(" ", _TAG_RE.sub(" ", doc)).strip(), MAX_DOC_CHARS)
    return ""


def _walk(node: object, prefix: str, out: set[str], depth: int = 0) -> None:
    if not isinstance(node, dict) or depth > 32:
        return
    for key in ("items", "additionalProperties"):
        if isinstance(node.get(key), dict):
            _walk(node[key], prefix, out, depth + 1)
    props = node.get("properties")
    if isinstance(props, dict):
        for name, child in props.items():
            path = f"{prefix}.{str(name).lower()}"
            out.add(path)
            _walk(child, path, out, depth + 1)


def crd_paths(controller_dir: Path, kind: str) -> set[str] | None:
    """The lowercased dotted spec.* and status.* paths of kind's CRD, or None
    when no CRD under config/crd/bases names kind."""
    base = controller_dir / "config" / "crd" / "bases"
    if not base.is_dir():
        return None
    for path in sorted(base.glob("*.yaml")):
        doc = yaml.safe_load(path.read_text())
        if not isinstance(doc, dict):
            continue
        spec = doc.get("spec") or {}
        if not isinstance(spec, dict) or (spec.get("names") or {}).get("kind") != kind:
            continue
        out: set[str] = set()
        for version in spec.get("versions") or []:
            schema = ((version or {}).get("schema") or {}).get("openAPIV3Schema") or {}
            props = schema.get("properties") or {}
            for top in ("spec", "status"):
                _walk(props.get(top), top, out)
        return out
    return None


def generator_config(controller_dir: Path) -> dict:
    path = controller_dir / "generator.yaml"
    if not path.is_file():
        return {}
    doc = yaml.safe_load(path.read_text())
    return doc if isinstance(doc, dict) else {}


def model_name(cfg: dict, service: str) -> str:
    """generator.yaml sdk_names.model_name, else the service alias (as the detector does)."""
    names = cfg.get("sdk_names")
    name = names.get("model_name") if isinstance(names, dict) else None
    return name if isinstance(name, str) and _MODEL_NAME_RE.fullmatch(name) else service


def resource_excerpt(cfg: dict, kind: str) -> str:
    resources = cfg.get("resources")
    entry = resources.get(kind) if isinstance(resources, dict) else None
    if not entry:
        return f"{kind}: no resources entry\n"
    return _cap(yaml.safe_dump({kind: entry}, sort_keys=True, default_flow_style=False), MAX_CONFIG_CHARS)


@dataclass
class Item:
    """One triage invocation, as the Dispatcher passed it (validated first)."""

    service: str
    resource: str
    field: str
    finding_class: str
    detector_work: str
    change_class: str
    sdk_to: str
    work_item: str


@dataclass
class EvidencePack:
    item: Item
    model: Model = field(repr=False)
    crd: set[str] | None = field(default=None, repr=False)
    field_found: bool = False
    member_shape: str = ""
    member_type: str = ""
    set_by: list[str] = field(default_factory=list)
    read_by: list[str] = field(default_factory=list)
    resource_operations: list[str] = field(default_factory=list)
    crd_has_field: bool = False
    crd_nearby: list[str] = field(default_factory=list)
    # Untrusted text: AWS-authored documentation and repository content.
    documentation: str = ""
    generator_excerpt: str = ""

    def ref_exists(self, ref: str) -> bool:
        """Whether an evidence reference names something real."""
        if not isinstance(ref, str) or not REF_RE.fullmatch(ref):
            return False
        kind, _, name = ref.partition(":")
        if kind == "op":
            return self.model.has_operation(name)
        if kind == "shape":
            return self.model.has_shape(name)
        if kind == "field":
            return self.model.find_field(name) is not None
        return self.crd is not None and name.lower() in self.crd


def build(item: Item, model: Model, controller_dir: Path) -> EvidencePack:
    crd = crd_paths(controller_dir, item.resource)
    pack = EvidencePack(item=item, model=model, crd=crd)
    found = model.find_field(item.field)
    if found:
        _, target, member = found
        pack.field_found = True
        pack.member_shape = target
        pack.member_type = model.type_of(target)
        pack.documentation = documentation(member, model.shapes.get(target))
        pack.set_by = model.set_by(item.field)
        pack.read_by = model.read_by(item.field)
    pack.resource_operations = model.resource_operations(item.resource)
    if crd is not None:
        lower = item.field.lower()
        pack.crd_has_field = f"spec.{lower}" in crd or f"status.{lower}" in crd
        first = lower.split(".", 1)[0]
        pack.crd_nearby = ["crd:" + p for p in sorted(crd) if p.split(".")[1:2] == [first]][:MAX_LISTED]
    pack.generator_excerpt = resource_excerpt(generator_config(controller_dir), item.resource)
    return pack
