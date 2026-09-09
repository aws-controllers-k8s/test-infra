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
"""Phase 3: E2E testing via test-infra (workflows/add-resource.md Phase 3).

Runs `make kind-test SERVICE=<service>` from the test-infra directory, classifies
the outcome (PASS / FAIL / SKIPPED), and applies the workflow's rules:

  - SKIPPED is treated as FAILURE — tests added by this workflow must actually
    execute via the bootstrap system, not gate on env vars.
  - On FAIL, dispatch the implementer with the failure details to fix, then
    re-run. Maximum `cfg.max_e2e_fix_attempts` (default 2) before escalating.

This phase is a fast-follow: the build-cluster ProwJob currently runs with
run_e2e=False (it lacks the privileged Docker-in-Docker + kind toolchain). The
code is kept intact and validated by the offline classifier/config tests so it
can be switched on once the build-cluster job grows a DinD/kind sidecar.
"""

from __future__ import annotations

import os
import re
import shutil
import subprocess
from dataclasses import dataclass, field
from pathlib import Path

import yaml

from .agents import AgentSet
from .config import Config

# Default: 45 minutes. E2E runs are 10-30+ minutes per the references.
_DEFAULT_E2E_TIMEOUT_S = 45 * 60

# pytest colorizes its summary ("\x1b[32m2 passed\x1b[0m"). The color escape
# leaves a letter (e.g. the `m` of `\x1b[32m`) immediately before the digit,
# which defeats the `\b` word boundary in the summary regexes below and made a
# genuinely-passing run classify as FAIL. Strip escapes before matching.
_ANSI_RE = re.compile(r"\x1b\[[0-9;]*m")


def _strip_ansi(text: str) -> str:
    return _ANSI_RE.sub("", text)


@dataclass
class E2EAttempt:
    """One `make kind-test` run and, if it failed and wasn't the last, the fix
    the implementer produced in response."""

    number: int
    status: str
    failure_excerpt: str = ""
    log_path: str = ""
    # The implementer's response to this attempt's failure (empty on the final
    # attempt or a PASS, where no fix is dispatched).
    fix_summary: str = ""


@dataclass
class E2EResult:
    status: str  # "PASS" | "FAIL" | "SKIPPED" | "NOT_RUN" | "ERROR"
    detail: str = ""
    attempts: int = 0
    artifacts_dir: str = ""
    fix_summaries: list[str] = field(default_factory=list)
    # One record per make kind-test run, in order — carries each run's outcome,
    # the extracted failure, the full-log artifact path, and the agent's fix.
    attempts_detail: list[E2EAttempt] = field(default_factory=list)


def _to_snake(resource: str) -> str:
    """ResourceName -> resource_name (matches test_<resource>.py naming)."""
    s = re.sub(r"(.)([A-Z][a-z]+)", r"\1_\2", resource)
    s = re.sub(r"([a-z0-9])([A-Z])", r"\1_\2", s)
    return s.lower()


def ensure_test_config(cfg: Config) -> tuple[bool, str]:
    """Ensure test_config.yaml exists and is configured for this resource's test.

    During a normal run the harness cannot know the test method name in advance,
    so it derives it (`test_<resource>`) and writes it into the config itself.
    Specifically this:
      - creates test_config.yaml from the example if missing;
      - sets tests.methods to [test_<resource>] so the run targets the resource
        this workflow added (overriding any stale prior filter);
      - enables debug.enabled + debug.dump_controller_logs so failures are
        diagnosable from $ARTIFACTS, per the Phase 3 SOP.

    It deliberately does NOT set aws.assumed_role_arn — that is environment
    specific and must already be present; we report it as a precondition rather
    than guessing a role ARN. Returns (ok, message).
    """
    ti = cfg.test_infra_dir
    if not ti.is_dir():
        return False, f"test-infra directory not found: {ti}"

    config_path = ti / "test_config.yaml"
    created = False
    if not config_path.is_file():
        example = ti / "test_config.example.yaml"
        if not example.is_file():
            return False, f"neither test_config.yaml nor test_config.example.yaml in {ti}"
        shutil.copyfile(example, config_path)
        created = True

    method = f"test_{_to_snake(cfg.resource)}"

    try:
        data = yaml.safe_load(config_path.read_text()) or {}
    except yaml.YAMLError as exc:
        return False, f"could not parse {config_path}: {exc}"

    # Target only this resource's test.
    tests = data.setdefault("tests", {})
    tests["methods"] = [method]

    # Ensure controller logs are dumped for diagnosis on failure.
    debug = data.setdefault("debug", {})
    debug["enabled"] = True
    debug["dump_controller_logs"] = True

    config_path.write_text(yaml.safe_dump(data, sort_keys=False, default_flow_style=False))

    role = (data.get("aws") or {}).get("assumed_role_arn")
    role_note = "" if role else (
        " WARNING: aws.assumed_role_arn is not set — the e2e run will fail until "
        "you configure a test role."
    )
    verb = "created and configured" if created else "configured"
    return True, f"{verb} {config_path} (tests.methods=[{method}], debug on).{role_note}"


def _classify(stdout: str, returncode: int) -> str:
    """Classify make kind-test output into PASS / FAIL / SKIPPED.

    The exit code is authoritative for failure: a pytest failure always
    propagates non-zero up through `make kind-test`, so `returncode != 0` never
    misses one. The pytest summary is consulted only on a clean exit, to tell
    PASS from a SKIPPED-only run.

    (The old `\\d+ failed` / "error"-substring heuristic was dropped: it was
    redundant with the returncode check and mis-fired on the common "0 failed"
    pytest summary form.)
    """
    if returncode != 0:
        return "FAIL"
    text = _strip_ansi(stdout).lower()
    if re.search(r"\b\d+\s+skipped\b", text) and not re.search(r"\b\d+\s+passed\b", text):
        # Per the workflow, skipped tests added by this workflow are failures.
        return "SKIPPED"
    if re.search(r"\b\d+\s+passed\b", text):
        return "PASS"
    # Clean exit but no recognizable pytest summary — treat as a failure.
    return "FAIL"


def _extract_failure(combined: str, max_lines: int = 120) -> str:
    """Pull the actionable failure out of a full `make kind-test` log.

    Prefer the pytest FAILURES / short-test-summary section (the actual assertion
    that failed) over a blind tail, which is usually docker-build noise. Falls
    back to the last `max_lines` when there is no recognizable pytest section
    (e.g. the run failed before pytest — kind bring-up, controller build, creds).
    """
    lines = _strip_ansi(combined).splitlines()
    start = None
    for i, ln in enumerate(lines):
        low = ln.lower()
        if "= failures =" in low or "= errors =" in low or "short test summary" in low:
            start = i
            break
    excerpt = lines[start:] if start is not None else lines[-max_lines:]
    if len(excerpt) > max_lines:
        excerpt = excerpt[:max_lines] + [
            "... (truncated; full output in $ARTIFACTS/e2e-attempt-*.log)"
        ]
    return "\n".join(excerpt).strip()


def _run_make_kind_test(cfg: Config, artifacts: Path) -> subprocess.CompletedProcess:
    env = dict(os.environ)
    env["ARTIFACTS"] = str(artifacts)
    timeout = cfg.extra.get("e2e_timeout_s", _DEFAULT_E2E_TIMEOUT_S)
    return subprocess.run(
        ["make", "kind-test", f"SERVICE={cfg.service}"],
        cwd=str(cfg.test_infra_dir),
        env=env,
        capture_output=True,
        text=True,
        timeout=timeout,
    )


def _fix_prompt(cfg: Config, status: str, output_tail: str, artifacts: Path) -> str:
    if status == "SKIPPED":
        return (
            f"The e2e test for {cfg.resource} in the {cfg.service} controller was "
            "SKIPPED. Skipped tests are NOT acceptable — a test that skips due to "
            "missing environment variables or unmet preconditions was written in a "
            "way that cannot execute in the test harness. Rewrite the test to use "
            "the bootstrap system (service_bootstrap.py / bootstrap_resources.py) or "
            "create resources in fixtures, following existing tests in "
            f"{cfg.controller_dir}/test/e2e/tests/. Do NOT gate execution on env vars.\n\n"
            f"Test output (tail):\n{output_tail}"
        )
    return (
        f"The e2e test for {cfg.resource} in the {cfg.service} controller FAILED. "
        f"Read the controller logs in {artifacts}/ and the test output below, "
        "diagnose the root cause, and fix it (generator.yaml, hooks, or the test "
        "itself — never generated files). Then report what you changed.\n\n"
        f"Test output (tail):\n{output_tail}"
    )


def run_e2e(cfg: Config, agents: AgentSet) -> E2EResult:
    """Run Phase 3 with up to cfg.max_e2e_fix_attempts implementer fix cycles."""
    ok, msg = ensure_test_config(cfg)
    if not ok:
        return E2EResult(status="NOT_RUN", detail=msg)

    artifacts = Path(os.environ.get("ARTIFACTS") or "/tmp/ack-test-logs")
    artifacts.mkdir(parents=True, exist_ok=True)

    result = E2EResult(status="NOT_RUN", detail=msg, artifacts_dir=str(artifacts))

    # 1 initial run + up to max_e2e_fix_attempts fix-and-rerun cycles.
    max_runs = cfg.max_e2e_fix_attempts + 1
    for attempt in range(1, max_runs + 1):
        result.attempts = attempt
        print(f"[e2e] attempt {attempt}/{max_runs}: running `make kind-test`...", flush=True)
        rec = E2EAttempt(number=attempt, status="ERROR")
        result.attempts_detail.append(rec)
        try:
            proc = _run_make_kind_test(cfg, artifacts)
        except subprocess.TimeoutExpired as exc:
            rec.status = result.status = "ERROR"
            rec.failure_excerpt = result.detail = (
                f"make kind-test timed out after {exc.timeout}s on attempt {attempt}"
            )
            print(f"[e2e] attempt {attempt}: ERROR (timeout)", flush=True)
            return result
        except FileNotFoundError:
            rec.status = result.status = "ERROR"
            rec.failure_excerpt = result.detail = (
                "`make` not found — is the test-infra toolchain installed?"
            )
            return result

        combined = (proc.stdout or "") + "\n" + (proc.stderr or "")
        status = _classify(combined, proc.returncode)
        rec.status = status
        # Persist the FULL run output plus the exit code / verdict so a non-zero
        # `make kind-test` is debuggable even when it prints no error (e.g. a
        # silent `set -e` abort or an EXIT-trap exit-code override). The harness
        # ERR trap logs the failing command into `combined`; this footer records
        # the resulting exit code. Prow uploads $ARTIFACTS to S3.
        log_path = artifacts / f"e2e-attempt-{attempt}.log"
        footer = f"\n=== make kind-test exit={proc.returncode} classified={status} ===\n"
        try:
            log_path.write_text(combined + footer)
            rec.log_path = str(log_path)
        except OSError:
            pass

        excerpt = _extract_failure(combined) if status != "PASS" else ""
        rec.failure_excerpt = excerpt
        result.status = status
        result.detail = excerpt or status
        print(f"[e2e] attempt {attempt}: {status} (exit={proc.returncode})"
              + (f" (full log: {log_path})" if rec.log_path else ""), flush=True)

        if status == "PASS":
            return result

        if attempt >= max_runs:
            result.detail = (
                f"E2E still {status} after {attempt} attempt(s); escalating to user.\n\n{excerpt}"
            )
            print(f"[e2e] exhausted {max_runs} attempt(s) still {status}; escalating.", flush=True)
            return result

        # Dispatch the implementer to fix (with the extracted failure, not blind
        # build noise), record its response, then loop to re-run.
        print(f"[e2e] attempt {attempt} {status}; dispatching implementer to fix...", flush=True)
        fix = str(agents.implementer(_fix_prompt(cfg, status, excerpt, artifacts)))
        rec.fix_summary = fix
        result.fix_summaries.append(fix)
        print(f"[e2e] implementer responded ({len(fix)} chars); re-running.", flush=True)

    return result
