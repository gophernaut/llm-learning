"""Generate a Go test file from a source file and optionally run `go test`.

Works with a base SmolLM3 checkpoint or a PEFT adapter directory.
"""

from __future__ import annotations

import argparse
import os
import re
import subprocess
import tempfile
import time
from pathlib import Path

import torch
from datasets import load_dataset
from transformers import AutoModelForCausalLM, AutoTokenizer

MODEL_ID = "HuggingFaceTB/SmolLM3-3B"
CODE_COL = "Code"
TEST_COL = "Unit Test - (Ground Truth)"

# Held-out test-split rows that compile as a standalone module.
EVAL_CASES = {
    "iotest_writer": {
        "split": "test",
        "index": 986,
        "module": "example.com/iotest",
        "source_name": "writer.go",
        "test_name": "writer_test.go",
    },
    "slowmessage": {
        "split": "test",
        "index": 898,
        "module": "example.com/slowmessage",
        "source_name": "slowmessage.go",
        "test_name": "slowmessage_test.go",
    },
    # Found with code/find_eval_cases.py: short, stdlib-only imports, and
    # verified to `go test` PASS standalone before ever touching the model.
    "k8s_allocator_utils": {
        "split": "test",
        "index": 22,
        "module": "example.com/case22",
        "source_name": "utils.go",
        "test_name": "utils_test.go",
    },
    "stdlib_errors": {
        "split": "test",
        "index": 119,
        "module": "example.com/case119",
        "source_name": "errors.go",
        "test_name": "errors_test.go",
    },
    "stdlib_http_mapping": {
        "split": "test",
        "index": 201,
        "module": "example.com/case201",
        "source_name": "mapping.go",
        "test_name": "mapping_test.go",
    },
    "stdlib_cover_html": {
        "split": "test",
        "index": 258,
        "module": "example.com/case258",
        "source_name": "html.go",
        "test_name": "html_test.go",
    },
    "stdlib_zstd_window": {
        "split": "test",
        "index": 341,
        "module": "example.com/case341",
        "source_name": "window.go",
        "test_name": "window_test.go",
    },
    "k8s_mux_discovery": {
        "split": "test",
        "index": 390,
        "module": "example.com/case390",
        "source_name": "mux_discovery_complete.go",
        "test_name": "mux_discovery_complete_test.go",
    },
    "stdlib_testing_allocs": {
        "split": "test",
        "index": 449,
        "module": "example.com/case449",
        "source_name": "allocs.go",
        "test_name": "allocs_test.go",
    },
    "k8s_webhook_resolver": {
        "split": "test",
        "index": 506,
        "module": "example.com/case506",
        "source_name": "serviceresolver.go",
        "test_name": "serviceresolver_test.go",
    },
    "k8s_configz": {
        "split": "test",
        "index": 611,
        "module": "example.com/case611",
        "source_name": "configz.go",
        "test_name": "configz_test.go",
    },
    "k8s_valuefuzz": {
        "split": "test",
        "index": 683,
        "module": "example.com/case683",
        "source_name": "valuefuzz.go",
        "test_name": "valuefuzz_test.go",
    },
    "tidb_hack": {
        "split": "test",
        "index": 705,
        "module": "example.com/case705",
        "source_name": "hack.go",
        "test_name": "hack_test.go",
    },
    "gin_bytesconv": {
        "split": "test",
        "index": 738,
        "module": "example.com/case738",
        "source_name": "bytesconv.go",
        "test_name": "bytesconv_test.go",
    },
}

FENCE_RE = re.compile(r"```(?:go)?\s*(.*?)```", re.DOTALL)
THINK_RE = re.compile(r"<think>.*?</think>", re.DOTALL)
PACKAGE_RE = re.compile(r"^package\s+\w+", re.MULTILINE)


def pick_device():
    if torch.cuda.is_available():
        return torch.device("cuda"), torch.bfloat16 if torch.cuda.is_bf16_supported() else torch.float16
    if torch.backends.mps.is_available():
        return torch.device("mps"), torch.float16
    return torch.device("cpu"), torch.float32


def load_case(name: str) -> dict:
    spec = EVAL_CASES[name]
    ds = load_dataset("Nutanix/GO-UNITTEST-BENCH", split=spec["split"])
    row = ds[spec["index"]]
    return {**spec, "row": row}


def build_messages(row: dict) -> list[dict]:
    user = (
        f"Write a Go unit test file for `{row['File Name']}` "
        f"({row['File path in Repository']}).\n\n"
        f"```go\n{row[CODE_COL]}\n```"
    )
    return [
        {"role": "system", "content": "/no_think"},
        {"role": "user", "content": user},
    ]


def extract_go(text: str) -> str:
    """Pull a Go file out of chatty model output.

    Handles a missing closing fence (common when generation hits max_new_tokens)
    and drops English that appears before `package`.
    """
    text = THINK_RE.sub("", text).strip()
    closed = re.search(r"```(?:go)?\s*\n?(.*?)```", text, re.DOTALL)
    if closed:
        block = closed.group(1)
    else:
        opened = re.search(r"```(?:go)?\s*\n?(.*)$", text, re.DOTALL)
        block = opened.group(1) if opened else text
    match = PACKAGE_RE.search(block)
    if match:
        block = block[match.start() :]
    return block.strip()


def generate_test(model, tokenizer, messages: list[dict], max_new_tokens: int = 1024) -> dict:
    prompt = tokenizer.apply_chat_template(
        messages,
        tokenize=False,
        add_generation_prompt=True,
        enable_thinking=False,
        clean_up_tokenization_spaces=False,
    )
    inputs = tokenizer(prompt, return_tensors="pt", clean_up_tokenization_spaces=False)
    inputs = {k: v.to(model.device) for k, v in inputs.items()}
    t0 = time.perf_counter()
    with torch.no_grad():
        out = model.generate(
            **inputs,
            max_new_tokens=max_new_tokens,
            temperature=0.2,
            top_p=0.9,
            do_sample=True,
        )
    elapsed = time.perf_counter() - t0
    new = out[0][inputs["input_ids"].shape[-1] :]
    raw = tokenizer.decode(new, skip_special_tokens=True, clean_up_tokenization_spaces=False)
    return {
        "raw": raw,
        "go": extract_go(raw),
        "seconds": elapsed,
        "new_tokens": int(new.shape[0]),
    }


def run_go_test(source: str, test_src: str, source_name: str, test_name: str, module: str) -> dict:
    with tempfile.TemporaryDirectory() as tmp:
        root = Path(tmp)
        (root / source_name).write_text(source, encoding="utf-8")
        (root / test_name).write_text(test_src, encoding="utf-8")
        init = subprocess.run(
            ["go", "mod", "init", module],
            cwd=root,
            capture_output=True,
            text=True,
        )
        test = subprocess.run(
            ["go", "test", "-count=1", "."],
            cwd=root,
            capture_output=True,
            text=True,
        )
        return {
            "returncode": test.returncode,
            "passed": test.returncode == 0,
            "stdout": test.stdout,
            "stderr": test.stderr,
            "mod_stderr": init.stderr,
        }


def load_model(model_id: str, adapter_dir: str | None = None):
    device, dtype = pick_device()
    tokenizer = AutoTokenizer.from_pretrained(adapter_dir or model_id)
    model = AutoModelForCausalLM.from_pretrained(model_id, dtype=dtype)
    if adapter_dir:
        from peft import PeftModel

        model = PeftModel.from_pretrained(model, adapter_dir)
    model = model.to(device)
    model.eval()
    return tokenizer, model, device


def evaluate_case(name: str, model, tokenizer, out_dir: Path, max_new_tokens: int = 1024) -> dict:
    case = load_case(name)
    row = case["row"]
    gen = generate_test(model, tokenizer, build_messages(row), max_new_tokens=max_new_tokens)
    gt = run_go_test(
        row[CODE_COL],
        row[TEST_COL],
        case["source_name"],
        case["test_name"],
        case["module"],
    )
    pred = run_go_test(
        row[CODE_COL],
        gen["go"],
        case["source_name"],
        case["test_name"],
        case["module"],
    )
    record = {
        "case": name,
        "repository": row["Repository"],
        "path": row["File path in Repository"],
        "seconds": gen["seconds"],
        "new_tokens": gen["new_tokens"],
        "ground_truth_pass": gt["passed"],
        "generated_pass": pred["passed"],
        "generated_stdout": pred["stdout"],
        "generated_stderr": pred["stderr"],
        "raw": gen["raw"],
        "extracted_go": gen["go"],
    }
    out_dir.mkdir(parents=True, exist_ok=True)
    md = [
        f"# Eval: {name}",
        "",
        f"- Repo: `{record['repository']}` `{record['path']}`",
        f"- Ground-truth `go test`: {'PASS' if gt['passed'] else 'FAIL'}",
        f"- Generated `go test`: {'PASS' if pred['passed'] else 'FAIL'}",
        f"- Generation: {gen['new_tokens']} tokens in {gen['seconds']:.1f}s",
        "",
        "## Extracted Go",
        "",
        "```go",
        gen["go"],
        "```",
        "",
        "## go test stdout/stderr",
        "",
        "```",
        (pred["stdout"] or "") + (pred["stderr"] or ""),
        "```",
        "",
        "## Raw model output",
        "",
        "```",
        gen["raw"],
        "```",
        "",
    ]
    (out_dir / f"{name}.md").write_text("\n".join(md), encoding="utf-8")
    return record


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--model-id", default=MODEL_ID)
    parser.add_argument("--adapter-dir", default=None)
    parser.add_argument("--cases", nargs="+", default=list(EVAL_CASES))
    parser.add_argument(
        "--out-dir",
        type=Path,
        default=Path(__file__).resolve().parents[1] / "transcripts" / "baseline",
    )
    parser.add_argument("--max-new-tokens", type=int, default=1024)
    args = parser.parse_args()

    tokenizer, model, device = load_model(args.model_id, args.adapter_dir)
    print(f"Loaded on {device}")
    for name in args.cases:
        rec = evaluate_case(name, model, tokenizer, args.out_dir, args.max_new_tokens)
        print(
            f"{name}: gt={'PASS' if rec['ground_truth_pass'] else 'FAIL'} "
            f"gen={'PASS' if rec['generated_pass'] else 'FAIL'} "
            f"{rec['new_tokens']} tok / {rec['seconds']:.1f}s"
        )


if __name__ == "__main__":
    main()
