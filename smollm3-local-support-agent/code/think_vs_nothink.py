"""Compare SmolLM3 /think vs /no_think on the same support prompt.

Saves a markdown transcript next to this file's parent folder.

Run from the repo root (venv activated):

    python Article_1/code/think_vs_nothink.py
"""

from __future__ import annotations

import re
import time
from pathlib import Path

import torch
from transformers import AutoModelForCausalLM, AutoTokenizer

MODEL_ID = "HuggingFaceTB/SmolLM3-3B"
PROMPT = (
    "A customer is charged twice for the same order. "
    "What are three concrete steps support should take?"
)

ROOT = Path(__file__).resolve().parents[1]
TRANSCRIPT_PATH = ROOT / "transcripts" / "think_vs_nothink.md"


def load_model():
    device = torch.device("mps" if torch.backends.mps.is_available() else "cpu")
    dtype = torch.float16 if device.type == "mps" else torch.bfloat16
    print(f"Loading {MODEL_ID} on {device} ({dtype})...")
    tokenizer = AutoTokenizer.from_pretrained(MODEL_ID)
    model = AutoModelForCausalLM.from_pretrained(MODEL_ID, dtype=dtype).to(device)
    model.eval()
    return tokenizer, model, device


def render_prompt(tokenizer, messages: list[dict]) -> str:
    return tokenizer.apply_chat_template(
        messages,
        tokenize=False,
        add_generation_prompt=True,
        clean_up_tokenization_spaces=False,
    )


def generate(tokenizer, model, messages: list[dict], max_new_tokens: int) -> dict:
    text = render_prompt(tokenizer, messages)
    inputs = tokenizer(text, return_tensors="pt", clean_up_tokenization_spaces=False)
    inputs = {k: v.to(model.device) for k, v in inputs.items()}
    prompt_tokens = inputs["input_ids"].shape[-1]

    t0 = time.perf_counter()
    with torch.no_grad():
        output_ids = model.generate(
            **inputs,
            max_new_tokens=max_new_tokens,
            temperature=0.6,
            top_p=0.95,
            do_sample=True,
        )
    elapsed = time.perf_counter() - t0

    new_tokens = output_ids[0][prompt_tokens:]
    raw = tokenizer.decode(
        new_tokens, skip_special_tokens=True, clean_up_tokenization_spaces=False
    )
    stripped = re.sub(r"<think>.*?</think>", "", raw, flags=re.DOTALL).strip()
    return {
        "prompt_tokens": prompt_tokens,
        "new_tokens": int(new_tokens.shape[0]),
        "seconds": elapsed,
        "tokens_per_sec": int(new_tokens.shape[0]) / elapsed if elapsed else 0.0,
        "raw": raw.strip(),
        "stripped": stripped,
    }


def fence(text: str) -> str:
    return f"```\n{text.rstrip()}\n```"


def main() -> None:
    tokenizer, model, device = load_model()

    no_think_messages = [
        {"role": "system", "content": "/no_think"},
        {"role": "user", "content": PROMPT},
    ]
    think_messages = [
        {"role": "system", "content": "/think"},
        {"role": "user", "content": PROMPT},
    ]

    print("Generating /no_think ...")
    no_think = generate(tokenizer, model, no_think_messages, max_new_tokens=256)
    print("Generating /think ...")
    think = generate(tokenizer, model, think_messages, max_new_tokens=1024)

    rendered_no_think = render_prompt(tokenizer, no_think_messages)
    rendered_think = render_prompt(tokenizer, think_messages)

    lines = [
        "# Think vs no_think transcript",
        "",
        f"- Model: `{MODEL_ID}`",
        f"- Device: `{device}`",
        f"- Prompt: {PROMPT}",
        "",
        "## Timing",
        "",
        "| Mode | Prompt tokens | New tokens | Seconds | Tokens/s |",
        "|---|---:|---:|---:|---:|",
        (
            f"| `/no_think` | {no_think['prompt_tokens']} | {no_think['new_tokens']} "
            f"| {no_think['seconds']:.1f} | {no_think['tokens_per_sec']:.1f} |"
        ),
        (
            f"| `/think` | {think['prompt_tokens']} | {think['new_tokens']} "
            f"| {think['seconds']:.1f} | {think['tokens_per_sec']:.1f} |"
        ),
        "",
        "## Rendered `/no_think` prompt (chat template)",
        "",
        fence(rendered_no_think),
        "",
        "## `/no_think` raw output",
        "",
        fence(no_think["raw"]),
        "",
        "## `/no_think` customer-facing (think block stripped)",
        "",
        fence(no_think["stripped"]),
        "",
        "## Rendered `/think` prompt (chat template)",
        "",
        fence(rendered_think),
        "",
        "## `/think` raw output",
        "",
        fence(think["raw"]),
        "",
        "## `/think` customer-facing (think block stripped)",
        "",
        fence(think["stripped"]),
        "",
    ]

    TRANSCRIPT_PATH.parent.mkdir(parents=True, exist_ok=True)
    TRANSCRIPT_PATH.write_text("\n".join(lines), encoding="utf-8")
    print(f"Wrote {TRANSCRIPT_PATH}")
    print(
        f"/no_think: {no_think['new_tokens']} tokens in {no_think['seconds']:.1f}s | "
        f"/think: {think['new_tokens']} tokens in {think['seconds']:.1f}s"
    )


if __name__ == "__main__":
    main()
