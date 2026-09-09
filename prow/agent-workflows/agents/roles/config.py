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
"""Runtime configuration for the role-based add-resource harness.

All paths and the model id resolve from explicit values, then environment
variables, then defaults derived from the shared `utils.settings` ACK workspace
layout. The ack-dev-skills directory is read at runtime (never vendored) so the
harness stays in sync with upstream role SOPs, schemas, and the ack-dev SKILL.

Path resolution is intentionally pure (no git/network). The workflow adapter is
responsible for materializing the checkouts (forking the controller, cloning
code-generator, mounting ack-dev-skills via Prow refs) before a run.
"""

from __future__ import annotations

import os
from dataclasses import dataclass, field
from pathlib import Path

from config.defaults import DEFAULT_MODEL_ID, DEFAULT_REGION, DEFAULT_TEMPERATURE

# ack-dev-skills is delivered to the workflow pod as a Prow extra_ref, cloned by
# the clonerefs init container. The generator sets ACK_DEV_SKILLS_DIR to that
# path; the sibling fallback is for local development next to the controller.
DEFAULT_SKILLS_DIRNAME = "ack-dev-skills"

# Loop bounds from workflows/add-resource.md.
DEFAULT_MAX_IMPL_ITERATIONS = 4
DEFAULT_MAX_REPLAN_ATTEMPTS = 1
DEFAULT_MAX_E2E_FIX_ATTEMPTS = 2

# Per-turn output token cap. The role agents emit large plan/review documents,
# so this is well above the 4000 the old task agents used.
DEFAULT_MAX_TOKENS = 8192

# Per-shell-command timeout (seconds). Must fit a legit `make build-controller`
# (~1-3 min) while capping pathological commands (a `find /` or recursive grep
# over a broad root). Enforced via the strands shell tool's SHELL_DEFAULT_TIMEOUT.
DEFAULT_SHELL_TIMEOUT_S = 300


def _first_env(*names: str) -> str | None:
    for name in names:
        val = os.environ.get(name)
        if val:
            return val
    return None


@dataclass
class Config:
    """Resolved configuration for one add-resource run."""

    service: str
    resource: str
    controller_dir: Path
    codegen_dir: Path
    skills_dir: Path
    # Default model id for every role. Per-role overrides below fall back to this
    # when unset, so `--model` alone changes all four agents.
    model_id: str = DEFAULT_MODEL_ID
    planner_model_id: str | None = None
    implementer_model_id: str | None = None
    reviewer_model_id: str | None = None
    # Sampling temperature. The shared Bedrock factory sends this to the model;
    # set to None only for a model that rejects the parameter.
    temperature: float | None = DEFAULT_TEMPERATURE
    max_tokens: int = DEFAULT_MAX_TOKENS
    region: str = DEFAULT_REGION
    shell_timeout_s: int = DEFAULT_SHELL_TIMEOUT_S
    # AWS SDK Go v2 *module* version used by `make build-controller`.
    aws_sdk_go_version: str | None = None
    # Phase toggles — Phase 3 (E2E) needs a kind cluster + AWS creds and is a
    # fast-follow; the build-cluster ProwJob currently runs with run_e2e=False.
    run_e2e: bool = False
    # Loop bounds from workflows/add-resource.md.
    max_impl_iterations: int = DEFAULT_MAX_IMPL_ITERATIONS
    max_replan_attempts: int = DEFAULT_MAX_REPLAN_ATTEMPTS
    max_e2e_fix_attempts: int = DEFAULT_MAX_E2E_FIX_ATTEMPTS
    extra: dict = field(default_factory=dict)

    @classmethod
    def resolve(
        cls,
        *,
        service: str,
        resource: str,
        controller_dir: str | os.PathLike | None = None,
        codegen_dir: str | os.PathLike | None = None,
        skills_dir: str | os.PathLike | None = None,
        model_id: str | None = None,
        planner_model_id: str | None = None,
        implementer_model_id: str | None = None,
        reviewer_model_id: str | None = None,
        temperature: float | None = None,
        max_tokens: int | None = None,
        region: str | None = None,
        aws_sdk_go_version: str | None = None,
        run_e2e: bool = False,
    ) -> "Config":
        """Build a Config, filling unset values from env vars then defaults.

        Path resolution:
        - controller_dir: arg -> $CONTROLLER_DIR -> current working directory.
          prow-job.sh sets $CONTROLLER_DIR to the forked controller checkout it
          later commits and pushes, so the implementer edits exactly that tree.
        - codegen_dir: arg -> $CODEGEN_DIR -> sibling `code-generator` of the
          controller's parent (the standard ACK workspace layout).
        - skills_dir: arg -> $ACK_DEV_SKILLS_DIR -> sibling `ack-dev-skills`.
        """
        controller = Path(
            controller_dir or _first_env("CONTROLLER_DIR") or os.getcwd()
        ).expanduser().resolve()

        workspace_root = controller.parent

        codegen = Path(
            codegen_dir
            or _first_env("CODEGEN_DIR")
            or workspace_root / "code-generator"
        ).expanduser().resolve()

        skills = Path(
            skills_dir
            or _first_env("ACK_DEV_SKILLS_DIR")
            or workspace_root / DEFAULT_SKILLS_DIRNAME
        ).expanduser().resolve()

        if temperature is None:
            temp_env = _first_env("AGENT_TEMPERATURE")
            temperature = float(temp_env) if temp_env is not None else DEFAULT_TEMPERATURE

        if max_tokens is None:
            max_tok_env = _first_env("AGENT_MAX_TOKENS")
            max_tokens = int(max_tok_env) if max_tok_env is not None else DEFAULT_MAX_TOKENS

        shell_timeout = DEFAULT_SHELL_TIMEOUT_S
        shell_env = _first_env("AGENT_SHELL_TIMEOUT")
        if shell_env is not None:
            shell_timeout = int(shell_env)

        return cls(
            service=service,
            resource=resource,
            controller_dir=controller,
            codegen_dir=codegen,
            skills_dir=skills,
            model_id=model_id or _first_env("AGENT_MODEL_ID") or DEFAULT_MODEL_ID,
            planner_model_id=planner_model_id or _first_env("AGENT_PLANNER_MODEL_ID"),
            implementer_model_id=implementer_model_id or _first_env("AGENT_IMPLEMENTER_MODEL_ID"),
            reviewer_model_id=reviewer_model_id or _first_env("AGENT_REVIEWER_MODEL_ID"),
            temperature=temperature,
            max_tokens=max_tokens,
            shell_timeout_s=shell_timeout,
            region=region or _first_env("AWS_REGION", "AWS_DEFAULT_REGION") or DEFAULT_REGION,
            aws_sdk_go_version=aws_sdk_go_version or _first_env("AWS_SDK_GO_VERSION"),
            run_e2e=run_e2e,
        )

    def validate_paths(self) -> list[str]:
        """Return a list of human-readable problems with the resolved paths.

        Empty list means everything required is present. The skills_dir is
        required (the harness cannot compose system prompts without it); the
        controller/codegen dirs are required for a real run.
        """
        problems: list[str] = []
        if not self.skills_dir.is_dir():
            problems.append(f"ack-dev-skills dir not found: {self.skills_dir}")
        else:
            skill_md = self.skills_dir / "skills" / "ack-dev" / "SKILL.md"
            if not skill_md.is_file():
                problems.append(f"ack-dev SKILL.md not found under: {self.skills_dir}")
        if not self.controller_dir.is_dir():
            problems.append(f"controller dir not found: {self.controller_dir}")
        if not self.codegen_dir.is_dir():
            problems.append(f"code-generator dir not found: {self.codegen_dir}")
        return problems

    @property
    def test_infra_dir(self) -> Path:
        """test-infra (Makefile + scripts for `make kind-test`) location.

        Prefer $TEST_INFRA_DIR (set from the test-infra Prow extra_ref); fall
        back to the sibling of the controller in the ACK workspace layout.
        """
        env = _first_env("TEST_INFRA_DIR")
        if env:
            return Path(env).expanduser().resolve()
        return self.controller_dir.parent / "test-infra"

    # Per-role model ids, falling back to the shared default.
    @property
    def planner_model(self) -> str:
        return self.planner_model_id or self.model_id

    @property
    def implementer_model(self) -> str:
        return self.implementer_model_id or self.model_id

    @property
    def reviewer_model(self) -> str:
        return self.reviewer_model_id or self.model_id
