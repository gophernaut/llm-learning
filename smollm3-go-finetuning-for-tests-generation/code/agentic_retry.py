"""Self-correcting retry loop on top of evaluate.py's harness.

Reuses evaluate.py rather than reinventing it: same 14 EVAL_CASES, same
`go test` scoring, same model-loading path. The only new thing here: instead
of scoring one generation, give the model up to `--max-attempts` turns. On
failure, the actual `go test` output goes back as the next user turn, and the
model gets to try again — the same loop a real coding agent runs, minus the
agent framework.

Works against the base model or either trained adapter
(`--adapter-dir outputs/practice` or `outputs/full_short`), so it can answer:
does a retry loop close the gap that more training didn't?
"""

from __future__ import annotations

import argparse
import json
from pathlib import Path

from evaluate import (
    CODE_COL,
    EVAL_CASES,
    MODEL_ID,
    build_messages,
    generate_test,
    load_case,
    load_model,
    run_go_test,
)

MAX_ATTEMPTS = 3
FEEDBACK_CHARS = 800


def run_case_with_retries(
    name: str,
    model,
    tokenizer,
    max_attempts: int = MAX_ATTEMPTS,
    max_new_tokens: int = 1024,
) -> dict:
    case = load_case(name)
    row = case["row"]
    messages = build_messages(row)
    attempts = []
    passed = False

    for attempt_no in range(1, max_attempts + 1):
        gen = generate_test(model, tokenizer, messages, max_new_tokens=max_new_tokens)
        result = run_go_test(
            row[CODE_COL], gen["go"], case["source_name"], case["test_name"], case["module"]
        )
        attempts.append(
            {
                "attempt": attempt_no,
                "passed": result["passed"],
                # Go sends panic traces to stdout, not stderr — keep both, not just stderr.
                "stdout": (result["stdout"] or "")[:FEEDBACK_CHARS],
                "stderr": (result["stderr"] or "")[:FEEDBACK_CHARS],
                "seconds": round(gen["seconds"], 1),
                "new_tokens": gen["new_tokens"],
                "go": gen["go"],
                "raw": gen["raw"],
            }
        )
        messages = messages + [{"role": "assistant", "content": gen["raw"]}]

        if result["passed"]:
            passed = True
            break

        if attempt_no < max_attempts:
            output = ((result["stderr"] or "") + (result["stdout"] or "")).strip()[:FEEDBACK_CHARS]
            feedback = (
                f"That test failed:\n\n{output}\n\n"
                "Fix the test file and return the complete corrected file."
            )
            messages = messages + [{"role": "user", "content": feedback}]

    return {
        "case": name,
        "passed": passed,
        "pass_attempt": attempts[-1]["attempt"] if passed else None,
        "attempts": attempts,
    }


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--model-id", default=MODEL_ID)
    parser.add_argument("--adapter-dir", default=None)
    parser.add_argument("--cases", nargs="+", default=list(EVAL_CASES))
    parser.add_argument("--max-attempts", type=int, default=MAX_ATTEMPTS)
    parser.add_argument("--max-new-tokens", type=int, default=1024)
    parser.add_argument("--tag", default="base", help="Label for output files, e.g. base/practice/full_short")
    parser.add_argument(
        "--out-dir",
        type=Path,
        default=None,
        help="Defaults to transcripts/agentic/<tag>/",
    )
    args = parser.parse_args()

    out_dir = args.out_dir or (
        Path(__file__).resolve().parents[1] / "transcripts" / "agentic" / args.tag
    )
    out_dir.mkdir(parents=True, exist_ok=True)

    tokenizer, model, device = load_model(args.model_id, args.adapter_dir)
    print(f"Loaded on {device}, tag={args.tag}, max_attempts={args.max_attempts}")

    results = []
    for name in args.cases:
        rec = run_case_with_retries(name, model, tokenizer, args.max_attempts, args.max_new_tokens)
        label = f"PASS@{rec['pass_attempt']}" if rec["passed"] else f"FAIL (all {len(rec['attempts'])})"
        print(f"{name}: {label}")
        (out_dir / f"{name}.json").write_text(json.dumps(rec, indent=2))
        results.append(rec)

    passed = sum(1 for r in results if r["passed"])
    print(f"\n{args.tag}: {passed}/{len(results)} passed within {args.max_attempts} attempts")

    summary = {
        "tag": args.tag,
        "max_attempts": args.max_attempts,
        "passed": passed,
        "total": len(results),
        "cases": [
            {"case": r["case"], "passed": r["passed"], "pass_attempt": r["pass_attempt"]}
            for r in results
        ],
    }
    (out_dir / "summary.json").write_text(json.dumps(summary, indent=2))
    print("wrote", out_dir / "summary.json")


if __name__ == "__main__":
    main()
