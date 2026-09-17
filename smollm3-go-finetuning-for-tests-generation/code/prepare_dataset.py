"""Inspect and format Nutanix/GO-UNITTEST-BENCH for SmolLM3 SFT.

This script does not load the model. It is safe to run on a laptop.
"""

from __future__ import annotations

import argparse
import json
from collections import Counter
from pathlib import Path

from datasets import Dataset, DatasetDict, load_dataset
from transformers import AutoTokenizer

MODEL_ID = "HuggingFaceTB/SmolLM3-3B"
CODE_COL = "Code"
TEST_COL = "Unit Test - (Ground Truth)"
# Practice subset: both source and test fit in a T4-friendly context.
SHORT_CHARS = 6000
# Original landmine bound from fine_tuning.py (MAX_LENGTH=4096 * 12).
MAX_SOURCE_CHARS = 4096 * 12


def format_example(example: dict) -> dict:
    user = (
        f"Write a Go unit test file for `{example['File Name']}` "
        f"({example['File path in Repository']}).\n\n"
        f"```go\n{example[CODE_COL]}\n```"
    )
    return {
        "prompt": [
            {"role": "system", "content": "/no_think"},
            {"role": "user", "content": user},
        ],
        "completion": [
            {"role": "assistant", "content": example[TEST_COL]},
        ],
        "chat_template_kwargs": {"enable_thinking": False},
        "repository": example["Repository"],
        "file_name": example["File Name"],
        "file_path": example["File path in Repository"],
        "code": example[CODE_COL],
        "unit_test": example[TEST_COL],
    }


def keep_short(example: dict, max_chars: int) -> bool:
    return bool(example[CODE_COL]) and bool(example[TEST_COL]) and (
        len(example[CODE_COL]) + len(example[TEST_COL]) <= max_chars
    )


def load_practice_splits(max_chars: int = SHORT_CHARS) -> DatasetDict:
    raw = load_dataset("Nutanix/GO-UNITTEST-BENCH")
    out = {}
    for split, rows in raw.items():
        n_raw = len(rows)
        rows = rows.filter(lambda ex: keep_short(ex, max_chars))
        rows = rows.map(format_example)
        print(f"{split}: {n_raw} -> {len(rows)} after char filter ({max_chars})")
        out[split] = rows
    return DatasetDict(out)


def inspect(out_dir: Path) -> dict:
    raw = load_dataset("Nutanix/GO-UNITTEST-BENCH")
    train = raw["train"]
    code_lens = sorted(len(r[CODE_COL]) for r in train)
    test_lens = sorted(len(r[TEST_COL]) for r in train)
    giant = max(train, key=lambda r: len(r[CODE_COL]))
    tok = AutoTokenizer.from_pretrained(MODEL_ID)

    def pct(xs, p):
        return xs[min(len(xs) - 1, int(round((p / 100) * (len(xs) - 1))))]

    stats = {
        "splits": {k: len(v) for k, v in raw.items()},
        "columns": train.column_names,
        "tokenizer_model_max_length": tok.model_max_length,
        "code_chars": {
            "p50": pct(code_lens, 50),
            "p90": pct(code_lens, 90),
            "p99": pct(code_lens, 99),
            "max": code_lens[-1],
        },
        "test_chars": {
            "p50": pct(test_lens, 50),
            "p90": pct(test_lens, 90),
            "p99": pct(test_lens, 99),
            "max": test_lens[-1],
        },
        "giant_row": {
            "repository": giant["Repository"],
            "file_name": giant["File Name"],
            "path": giant["File path in Repository"],
            "code_chars": len(giant[CODE_COL]),
            "test_chars": len(giant[TEST_COL]),
            "head": giant[CODE_COL][:180],
        },
        "repos": Counter(r["Repository"] for r in train).most_common(10),
        "filters": {},
    }
    for split, rows in raw.items():
        n = len(rows)
        stats["filters"][split] = {
            "raw": n,
            f"code+test<={SHORT_CHARS}": sum(
                1 for r in rows if len(r[CODE_COL]) + len(r[TEST_COL]) <= SHORT_CHARS
            ),
            f"code+test<={MAX_SOURCE_CHARS}": sum(
                1 for r in rows if len(r[CODE_COL]) + len(r[TEST_COL]) <= MAX_SOURCE_CHARS
            ),
        }
    out_dir.mkdir(parents=True, exist_ok=True)
    (out_dir / "dataset_stats.json").write_text(json.dumps(stats, indent=2), encoding="utf-8")
    print(json.dumps(stats, indent=2)[:4000])
    return stats


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--inspect-only", action="store_true")
    parser.add_argument("--out-dir", type=Path, default=Path(__file__).resolve().parents[1] / "transcripts")
    args = parser.parse_args()
    inspect(args.out_dir)
    if not args.inspect_only:
        splits = load_practice_splits()
        print("practice train/val/test", {k: len(v) for k, v in splits.items()})


if __name__ == "__main__":
    main()
