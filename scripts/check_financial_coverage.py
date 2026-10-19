#!/usr/bin/env python3
"""Enforce 100% branch coverage for the B02 financial modules.

The JSON input is produced by:

    cargo llvm-cov --branch -p nexora-types --all-features --json \
        --output-path coverage.json

Only source files in scope are required to be perfect. The full measured
workspace is printed for transparency, but identifiers, timestamps, and the
error registry are not part of the financial-math coverage gate.
"""

from __future__ import annotations

import json
import sys
from pathlib import Path
from typing import Any

FINANCIAL_MODULES = (
    "convert.rs",
    "decimal.rs",
    "fee.rs",
    "instrument.rs",
    "money.rs",
    "rounding.rs",
)
TARGET_PERCENT = 100.0


def branch_part(file_entry: dict[str, Any]) -> dict[str, Any]:
    return file_entry.get("summary", {}).get("branches", {})


def is_financial(filename: str) -> bool:
    normalized = filename.replace("\\", "/")
    return any(normalized.endswith(f"/src/{module}") for module in FINANCIAL_MODULES)


def branch_key(branch: Any) -> str:
    """Return the stable source location that identifies a branch region."""
    if isinstance(branch, dict):
        return f"{branch.get('line_start', '?')}:{branch.get('column_start', '?')}"
    if isinstance(branch, (list, tuple)) and len(branch) >= 2:
        return f"{branch[0]}:{branch[1]}"
    return "?"


def branch_executed(branch: Any) -> bool:
    """Whether the true edge of a branch region executed at least once.

    ``llvm-cov export`` emits list entries shaped like
    ``[line_start, column_start, line_end, column_end, execution_count,
    fallthrough_count, ...]``. llvm-cov reports a branch region as covered when
    its direct edge executed, so ``execution_count > 0`` is the predicate.
    """
    if isinstance(branch, dict):
        return int(branch.get("execution_count", branch.get("count", 0))) > 0
    if isinstance(branch, (list, tuple)) and len(branch) > 4:
        return int(branch[4]) > 0
    return False


def main() -> int:
    if len(sys.argv) != 2:
        print(f"usage: {Path(sys.argv[0]).name} coverage.json", file=sys.stderr)
        return 2

    report_path = Path(sys.argv[1])
    with report_path.open(encoding="utf-8") as handle:
        report = json.load(handle)

    files: list[dict[str, Any]] = []
    for data in report.get("data", []):
        files.extend(data.get("files", []))

    selected = [entry for entry in files if is_financial(str(entry.get("filename", "")))]
    found_modules = {
        Path(str(entry.get("filename", "")).replace("\\", "/")).name for entry in selected
    }
    missing = sorted(set(FINANCIAL_MODULES) - found_modules)
    if missing:
        print(f"ERROR: financial modules missing from coverage report: {', '.join(missing)}")
        return 1

    total_count = 0
    total_covered = 0
    print("Financial branch coverage:")
    for entry in sorted(selected, key=lambda item: str(item.get("filename", ""))):
        filename = Path(str(entry["filename"]).replace("\\", "/")).name
        branches = branch_part(entry)
        count = int(branches.get("count", 0))
        covered = int(branches.get("covered", 0))
        percent = float(branches.get("percent", 0.0))
        total_count += count
        total_covered += covered
        print(f"  {filename:<16} {percent:6.2f}% ({covered}/{count})")

    total_percent = (100.0 * total_covered / total_count) if total_count else 0.0
    print(f"  {'TOTAL':<16} {total_percent:6.2f}% ({total_covered}/{total_count})")

    all_count = sum(int(branch_part(entry).get("count", 0)) for entry in files)
    all_covered = sum(int(branch_part(entry).get("covered", 0)) for entry in files)
    all_percent = (100.0 * all_covered / all_count) if all_count else 0.0
    print(f"All measured files: {all_percent:.2f}% ({all_covered}/{all_count})")

    uncovered: list[str] = []
    for entry in selected:
        filename = str(entry.get("filename", ""))
        # The same source region can appear once per instrumented test binary,
        # so a region is only uncovered when no instantiation executed it.
        executed: dict[str, bool] = {}
        for branch in entry.get("branches", []):
            key = branch_key(branch)
            executed[key] = executed.get(key, False) or branch_executed(branch)
        for key, was_executed in executed.items():
            if not was_executed:
                uncovered.append(f"{filename}:{key}")

    if uncovered:
        print("Uncovered financial branches:")
        for location in uncovered:
            print(f"  {location}")

    if total_count == 0:
        print("ERROR: branch coverage was not collected")
        return 1
    if total_covered * 100 < total_count * TARGET_PERCENT:
        print(f"FAIL: financial branch coverage is below {TARGET_PERCENT:.2f}%")
        return 1

    print("PASS: financial branch coverage is 100%")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
