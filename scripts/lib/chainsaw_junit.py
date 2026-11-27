#!/usr/bin/env python3
"""Turn Chainsaw's step-level JUnit report into one row per test for Prow.

Chainsaw's JUNIT-TEST report has one test case per test, so a failure doesn't
say which part of the test failed: the create, an update, the delete. Its
JUNIT-STEP report has one test case per step, but names each after the step
alone, and Prow's JUnit lens shows a row as "classname: name", without the
suite, so a failure there reads ": check-update-1".

This reads the JUNIT-STEP report and writes one test case per test:

- A failed test is named "<test>: <first failed step>", and its failure message
  says which step failed, what the step does (its description in the test, if
  it has one, or for a generated check-update-N step, the update it checks), and
  the error. Any later failed step, e.g. a cleanup, is listed
  after it.
- A test that passed or was skipped is named "<resource>: <test>".
- The failure's message is one short line: each field that differs, as
  "<field>: expected <value>, got <value>". Its text, which Prow shows under
  the message in the row, keeping line breaks, has the whole error laid out a
  field per line, with JSON values indented, then the test's steps, each with
  its result and duration.
- A skipped test's reason is taken from its description, where ack-generate
  writes "Skipped: <reason>", since Chainsaw reports skips with no message.

Usage: chainsaw_junit.py <JUNIT-STEP report> <test dir> <output report>
"""

import json
import os
import re
import shutil
import subprocess
import sys
import xml.etree.ElementTree as ET

SKIPPED_MARKER = "Skipped: "


def load_test_docs(test_dir):
    """Returns each Chainsaw test's description and step descriptions, by
    test name, from the chainsaw-test.yaml files under test_dir. They only add
    detail, so without yq, or with a file yq can't read, they're left out."""
    docs = {}
    if not shutil.which("yq"):
        return docs
    query = (
        'select(.kind == "Test") | {"name": .metadata.name, '
        '"description": (.spec.description // ""), '
        '"steps": [(.spec.steps // [])[] | {"name": .name, "description": (.description // "")}]}'
    )
    for root, _, files in os.walk(test_dir):
        for f in files:
            if f != "chainsaw-test.yaml":
                continue
            try:
                out = subprocess.run(
                    ["yq", "-o=json", "-I=0", query, os.path.join(root, f)],
                    check=True, capture_output=True, text=True,
                ).stdout
            except (subprocess.CalledProcessError, OSError):
                continue
            for line in out.splitlines():
                line = line.strip()
                if not line:
                    continue
                try:
                    doc = json.loads(line)
                except ValueError:
                    continue
                docs[doc.get("name")] = {
                    "description": doc.get("description") or "",
                    "steps": {s.get("name"): s.get("description") or "" for s in doc.get("steps") or []},
                }
    return docs


def step_description(steps, name):
    """Returns what a step does: its own description, or for a generated
    check-update-N step, which has none, the update it checks."""
    if steps.get(name):
        return steps[name]
    if name.startswith("check-update-"):
        update = name[len("check-"):]
        if steps.get(update):
            return "after %s: %s" % (update, steps[update])
    return ""


def seconds(value):
    try:
        return float(value or 0)
    except ValueError:
        return 0.0


# Chainsaw's messages for a step that ran out of time say nothing about time
TIMEOUT_HINTS = {
    "signal: killed": "Chainsaw killed the step's command, which usually means it ran past its timeout",
}


def failure_of(case):
    """Returns a step's failure or error message, or None if it passed."""
    for tag in ("failure", "error"):
        el = case.find(tag)
        if el is not None:
            message = el.get("message") or ""
            hint = TIMEOUT_HINTS.get(message.strip())
            if hint:
                message = "%s (%s)" % (message, hint)
            parts = [message, (el.text or "").strip()]
            return "\n".join(p for p in parts if p) or "(no message)"
    return None


# A field error, as Chainsaw reports a check's mismatch: k8s field.Error text
FIELD_ERROR = re.compile(
    r"(?P<path>\S.*?): (?P<kind>Invalid value|Required value|Unsupported value|"
    r"Not found|Duplicate value|Forbidden|Too long|Too many|Internal error)(?:: (?P<rest>.*))?$",
    re.S,
)
# Where one field error ends and the next begins, in a list of them
NEXT_FIELD_ERROR = re.compile(
    r", (?=\S[^,]*?: (?:Invalid value|Required value|Unsupported value|Not found|"
    r"Duplicate value|Forbidden|Too long|Too many|Internal error))"
)
# The prefix every generated check's paths carry
JSON_PARSE_PREFIX = "(json_parse($stdout))."
# How many fields the one-line summary names before "and N more"
SUMMARY_FIELDS = 3
# How long a value can be in the one-line summary
SUMMARY_VALUE_LEN = 80


def field_errors(text):
    """Returns the field errors in a Chainsaw message, or [] if it isn't one:
    one error, a [list, of, them], or an assert's "* <error>" lines."""
    text = text.strip()
    if "\n" in text:
        items = [l[2:] for l in text.splitlines() if l.startswith("* ")]
    elif text.startswith("[") and text.endswith("]"):
        items = NEXT_FIELD_ERROR.split(text[1:-1])
    else:
        items = [text]
    errors = []
    for item in items:
        m = FIELD_ERROR.match(item.strip())
        if not m:
            return []
        path = m.group("path")
        if path.startswith(JSON_PARSE_PREFIX):
            path = path[len(JSON_PARSE_PREFIX):]
        got, want, detail = None, None, None
        rest = m.group("rest") or ""
        if m.group("kind") == "Invalid value":
            got, _, after = rest.partition(": ")
            if after.startswith("Expected value: "):
                want = after[len("Expected value: "):]
            else:
                detail = after or None
        else:
            detail = ("%s: %s" % (m.group("kind"), rest)) if rest else m.group("kind")
        errors.append({"path": path, "got": got, "want": want, "detail": detail})
    return errors


def as_json(value, indent=2):
    """Returns a value from a Chainsaw message as JSON, if it is JSON:
    indented, or with indent=None, on one line."""
    if value is None:
        return None
    if value == "<nil>":
        return "null"
    try:
        return json.dumps(json.loads(value), indent=indent, separators=None if indent else (",", ":"))
    except ValueError:
        return value


def clip(value):
    value = " ".join(value.split())
    if len(value) > SUMMARY_VALUE_LEN:
        return value[:SUMMARY_VALUE_LEN] + "..."
    return value


def summarize(message):
    """Returns a failure message as one short line."""
    errors = field_errors(message)
    if not errors:
        lines = [l.strip() for l in message.splitlines() if l.strip() and not l.startswith("---")]
        if len(message.strip().splitlines()) == 1:
            return message.strip()
        return clip(lines[0] if lines else message) + " (more below)"
    parts = []
    for e in errors[:SUMMARY_FIELDS]:
        path = clip(e["path"])
        if e["want"] is not None:
            parts.append("%s: expected %s, got %s" % (path, clip(as_json(e["want"], None)), clip(as_json(e["got"], None))))
        elif e["got"] is not None:
            parts.append("%s: %s, got %s" % (path, e["detail"] or "invalid", clip(as_json(e["got"], None))))
        else:
            parts.append("%s: %s" % (path, e["detail"]))
    if len(errors) > SUMMARY_FIELDS:
        parts.append("and %d more, below" % (len(errors) - SUMMARY_FIELDS))
    return "; ".join(parts)


def indented(text, prefix):
    return "\n".join(prefix + l for l in text.splitlines())


def detail(message):
    """Returns a failure message laid out a field per line, with JSON values
    indented. An assert's message keeps its resource header and diff."""
    errors = field_errors(message)
    if not errors:
        return message.rstrip()
    out = []
    if "\n" in message:
        # The resource header, before the "* " lines
        header = [l for l in message.splitlines() if l and not l.startswith(("* ", "---", "+++", "@@", " ", "-", "+"))]
        out.extend(header[:1])
    for e in errors:
        out.append(e["path"])
        if e["want"] is not None:
            out.append(_value_lines("expected", e["want"]))
            out.append(_value_lines("got", e["got"]))
        else:
            if e["detail"]:
                out.append("    " + e["detail"])
            if e["got"] is not None:
                out.append(_value_lines("got", e["got"]))
    if "\n" in message:
        diff = message[message.find("\n--- expected"):] if "\n--- expected" in message else ""
        if diff:
            out.append(diff.rstrip())
    return "\n".join(out)


def _value_lines(label, value):
    """Returns "    <label>: <value>", the values of expected and got lined up,
    and a multi-line JSON value's lines under its first."""
    text = as_json(value)
    lines = text.splitlines() or [""]
    head = "    %-9s " % (label + ":")
    return "\n".join([head + lines[0]] + [" " * len(head) + l for l in lines[1:]])


def skip_reason(description):
    """Returns the reason in a test description ack-generate wrote for a
    skipped test, or the whole description if it has no marker."""
    if SKIPPED_MARKER in description:
        return description.split(SKIPPED_MARKER, 1)[1].strip()
    return description.strip()


def convert(report_path, test_dir, out_path):
    root = ET.parse(report_path).getroot()
    docs = load_test_docs(test_dir)

    cases = []
    counts = {"tests": 0, "failures": 0, "skipped": 0}
    total_time = 0.0
    for suite in root.iter("testsuite"):
        steps = suite.findall("testcase")
        if not steps:
            continue
        test = suite.get("name") or ""
        # Chainsaw's package is the test's directory, as given to it: the
        # resource's directory, under however the test dir was spelled
        resource = os.path.basename(os.path.normpath(suite.get("package") or "")) or test
        doc = docs.get(test, {"description": "", "steps": {}})
        duration = seconds(suite.get("time"))
        total_time += duration
        counts["tests"] += 1

        case = ET.Element("testcase", {"time": "%f" % duration})
        skipped = suite.get("skipped") not in (None, "", "0") or any(s.find("skipped") is not None for s in steps)

        lines = []
        failed = []
        for step in steps:
            name = step.get("name") or ""
            msg = failure_of(step)
            status = "skipped" if step.find("skipped") is not None else ("FAILED" if msg else "passed")
            lines.append("%-8s %-28s %7.1fs" % (status, name, seconds(step.get("time"))))
            if msg:
                failed.append((name, msg))

        details = []
        if failed:
            first, msg = failed[0]
            case.set("classname", test)
            case.set("name", first)
            text = None
            for name, err in failed:
                what = step_description(doc["steps"], name)
                where = "%s%s" % (name, " (%s)" % what if what else "")
                if text is None:
                    text = "Failed at %s: %s" % (where, summarize(err))
                else:
                    text += ". Also failed at %s" % name
                # Details that would only repeat the summary are left out
                body = indented(detail(err), "  ").rstrip()
                if detail(err).strip() == summarize(err).strip():
                    body = ""
                # The message already names the first failed step
                if not details:
                    details.append(("%s\n" % body) if body else "")
                else:
                    details.append("Also failed at %s\n%s" % (where, ("\n%s\n" % body) if body else ""))
            # Prow shows the failure's text under its message, in the row,
            # keeping its line breaks
            el = ET.SubElement(case, "failure", {"message": text})
            el.text = "\n\n" + "\n".join([d for d in details if d] + ["Steps:", indented("\n".join(lines), "  ")]) + "\n"
            counts["failures"] += 1
        else:
            case.set("classname", resource)
            case.set("name", test)
            if skipped:
                reason = skip_reason(doc["description"])
                ET.SubElement(case, "skipped", {"message": reason})
                counts["skipped"] += 1

        cases.append(case)

    attrs = {k: str(v) for k, v in counts.items()}
    # The tests run in parallel, so the run's own time, not their sum
    attrs["time"] = "%f" % (seconds(root.get("time")) or total_time)
    out_root = ET.Element("testsuites", dict(attrs, name="chainsaw"))
    out_suite = ET.SubElement(out_root, "testsuite", dict(attrs, name="chainsaw"))
    out_suite.extend(cases)
    ET.indent(out_root)
    ET.ElementTree(out_root).write(out_path, encoding="utf-8", xml_declaration=True)
    return counts


def main(argv):
    if len(argv) != 4:
        sys.stderr.write(__doc__.split("\n\n")[-1] + "\n")
        return 2
    counts = convert(argv[1], argv[2], argv[3])
    print("Wrote %s: %d tests, %d failed, %d skipped" % (
        argv[3], counts["tests"], counts["failures"], counts["skipped"]))
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
