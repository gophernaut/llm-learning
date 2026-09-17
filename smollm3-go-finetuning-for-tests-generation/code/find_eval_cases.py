"""Search Nutanix/GO-UNITTEST-BENCH for more standalone eval cases.

Usage:
    python smollm3-go-finetuning-for-tests-generation/code/find_eval_cases.py [--split test] [--limit 20]
"""

from __future__ import annotations

import argparse
import re
import subprocess
import tempfile
from pathlib import Path

from datasets import load_dataset

CODE_COL = "Code"
TEST_COL = "Unit Test - (Ground Truth)"
SHORT_CHARS = 6000

# Already in EVAL_CASES — skip these.
EXCLUDE_INDICES = {("test", 986), ("test", 898)}

IMPORT_BLOCK_RE = re.compile(r"import\s*\((.*?)\)", re.DOTALL)
IMPORT_SINGLE_RE = re.compile(r'import\s+"([^"]+)"')
QUOTED_RE = re.compile(r'"([^"]+)"')


def extract_imports(code: str) -> list[str]:
    paths = []
    block = IMPORT_BLOCK_RE.search(code)
    if block:
        paths += QUOTED_RE.findall(block.group(1))
    paths += IMPORT_SINGLE_RE.findall(code)
    return paths


def is_stdlib_only(*sources: str) -> bool:
    for src in sources:
        for path in extract_imports(src):
            # Stdlib import paths never contain a dot in the first segment
            # (fmt, encoding/json); third-party ones do (github.com/..., k8s.io/...).
            first = path.split("/", 1)[0]
            if "." in first:
                return False
    return True


def run_go_test(code: str, test: str, source_name: str, test_name: str, module: str) -> dict:
    with tempfile.TemporaryDirectory() as tmp:
        root = Path(tmp)
        (root / source_name).write_text(code, encoding="utf-8")
        (root / test_name).write_text(test, encoding="utf-8")
        subprocess.run(["go", "mod", "init", module], cwd=root, capture_output=True, text=True)
        p = subprocess.run(["go", "test", "-count=1", "."], cwd=root, capture_output=True, text=True)
        return {"passed": p.returncode == 0, "stderr": p.stderr}


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--split", default="test")
    parser.add_argument("--limit", type=int, default=20, help="stop after this many verified PASS cases")
    args = parser.parse_args()

    ds = load_dataset("Nutanix/GO-UNITTEST-BENCH", split=args.split)
    found = []
    checked = 0
    for i, row in enumerate(ds):
        if (args.split, i) in EXCLUDE_INDICES:
            continue
        code, test = row[CODE_COL], row[TEST_COL]
        if not code or not test:
            continue
        if len(code) + len(test) > SHORT_CHARS:
            continue
        if "func Test" not in test:
            continue
        if not is_stdlib_only(code, test):
            continue

        checked += 1
        source_name = row["File Name"]
        if not source_name.endswith(".go"):
            continue
        test_name = source_name[:-3] + "_test.go"
        module = f"example.com/case{i}"
        result = run_go_test(code, test, source_name, test_name, module)
        status = "PASS" if result["passed"] else "fail"
        print(f"[{args.split}:{i}] {row['Repository']} {source_name} chars={len(code)+len(test)} -> {status}")
        if result["passed"]:
            found.append(
                {
                    "split": args.split,
                    "index": i,
                    "repository": row["Repository"],
                    "path": row["File path in Repository"],
                    "module": module,
                    "source_name": source_name,
                    "test_name": test_name,
                }
            )
        if len(found) >= args.limit:
            break

    print(f"\nChecked {checked} stdlib-only/short candidates, found {len(found)} standalone PASS")
    for f in found:
        print(f)


if __name__ == "__main__":
    main()
