"""Same agent as support_agent.py, with two product-level guards.

1. Execute a tool call only if the whole turn is a <tool_call> (no mixed prose).
2. Reject order IDs that are not ORD-XXXX style, so placeholders never hit the DB.

Customer-facing replies have any leftover <tool_call> blocks stripped.

Run from the repo root (venv activated):

    python Article_1/code/support_agent_guarded.py
"""

from __future__ import annotations

import json
import re
import time
from pathlib import Path

from mlx_lm import generate, load
from mlx_lm.sample_utils import make_sampler

MODEL_ID = "mlx-community/SmolLM3-3B-8bit"
ROOT = Path(__file__).resolve().parents[1]
TRANSCRIPT_PATH = ROOT / "transcripts" / "support_agent_guarded.md"

TOOLS = [
    {
        "name": "lookup_order_status",
        "description": (
            "Look up the current status, estimated delivery date, and carrier "
            "for a specific customer order. Call this only when the customer "
            "has provided a concrete order ID such as ORD-123456."
        ),
        "parameters": {
            "type": "object",
            "properties": {
                "order_id": {
                    "type": "string",
                    "description": "The order ID, in the format ORD-XXXXXX.",
                }
            },
            "required": ["order_id"],
        },
    }
]

ORDERS = {
    "ORD-4821": {"status": "shipped", "eta": "June 18, 2026", "carrier": "DHL"},
    "ORD-3307": {"status": "processing", "eta": "June 20, 2026", "carrier": None},
    "ORD-1190": {"status": "delivered", "eta": None, "carrier": "FedEx"},
}

QUERIES = [
    "Where is my order ORD-4821? It's been a week.",
    "My order ORD-3307 hasn't shipped yet -- what's the status?",
    "I just want to change my email address.",
    "Where is order ORD-9999?",
]

SAMPLER = make_sampler(temp=0.3, top_p=0.9)
ORDER_RE = re.compile(r"^ORD-\d{4,}$")
THINK_RE = re.compile(r"<think>.*?</think>", flags=re.DOTALL)
TOOL_RE = re.compile(r"<tool_call>(.*?)</tool_call>", flags=re.DOTALL)
PURE_TOOL_RE = re.compile(
    r"^\s*<tool_call>(.*?)</tool_call>\s*$", flags=re.DOTALL
)


def lookup_order_status(order_id: str) -> dict:
    return ORDERS.get(order_id, {"status": "not_found", "eta": None, "carrier": None})


def strip_think(text: str) -> str:
    return THINK_RE.sub("", text).strip()


def strip_tool_calls(text: str) -> str:
    return TOOL_RE.sub("", text).strip()


def parse_executable_tool_call(output: str):
    """Return (name, args, skip_reason). skip_reason is set when we refuse to run."""
    cleaned = strip_think(output)
    match = PURE_TOOL_RE.match(cleaned)
    if not match:
        return None, None, "not_a_pure_tool_call"
    try:
        payload = json.loads(match.group(1).strip())
    except json.JSONDecodeError:
        return None, None, "invalid_json"
    name = payload.get("name")
    args = payload.get("arguments") or {}
    if name != "lookup_order_status":
        return None, None, f"unknown_tool:{name}"
    order_id = str(args.get("order_id", "")).strip()
    if not ORDER_RE.match(order_id):
        return None, None, f"invalid_order_id:{order_id}"
    return name, args, None


def generate_reply(model, tokenizer, messages: list[dict]) -> str:
    prompt = messages
    if tokenizer.chat_template is not None:
        prompt = tokenizer.apply_chat_template(
            messages,
            xml_tools=TOOLS,
            enable_thinking=False,
            add_generation_prompt=True,
            tokenize=False,
        )
    return generate(
        model,
        tokenizer,
        prompt=prompt,
        max_tokens=256,
        sampler=SAMPLER,
        verbose=False,
    )


def respond_with_tools(model, tokenizer, user_message: str) -> dict:
    messages = [{"role": "user", "content": user_message}]
    t0 = time.perf_counter()
    turn1 = generate_reply(model, tokenizer, messages)
    tool_name, tool_args, skip_reason = parse_executable_tool_call(turn1)

    record = {
        "user": user_message,
        "turn1": turn1.strip(),
        "tool_name": tool_name,
        "tool_args": tool_args,
        "skip_reason": skip_reason,
        "tool_result": None,
        "final": None,
        "seconds": 0.0,
    }

    if tool_name == "lookup_order_status":
        tool_result = lookup_order_status(**tool_args)
        record["tool_result"] = tool_result
        messages += [
            {"role": "assistant", "content": turn1},
            {"role": "tool", "content": json.dumps(tool_result), "name": tool_name},
        ]
        record["final"] = strip_tool_calls(
            strip_think(generate_reply(model, tokenizer, messages))
        )
    else:
        record["final"] = strip_tool_calls(strip_think(turn1))

    record["seconds"] = time.perf_counter() - t0
    return record


def fence(text: str) -> str:
    return f"```\n{text.rstrip()}\n```"


def main() -> None:
    print(f"Loading {MODEL_ID}...")
    model, tokenizer = load(MODEL_ID)

    records = []
    for query in QUERIES:
        print(f"Customer: {query}")
        record = respond_with_tools(model, tokenizer, query)
        records.append(record)
        if record["tool_name"]:
            print(f"  tool: {record['tool_name']}({record['tool_args']}) -> {record['tool_result']}")
        else:
            print(f"  tool: skipped ({record['skip_reason']})")
        print(f"  {record['seconds']:.1f}s")

    lines = [
        "# Guarded support agent transcript",
        "",
        f"- Model: `{MODEL_ID}`",
        "- Guards: pure `<tool_call>` only; `ORD-\\d{4,}` order IDs; strip tool XML from replies",
        "",
    ]

    for i, rec in enumerate(records, start=1):
        lines += [
            f"## Query {i}",
            "",
            f"**Customer:** {rec['user']}",
            "",
            f"- Seconds: {rec['seconds']:.1f}",
            (
                f"- Tool called: `{rec['tool_name']}`"
                if rec["tool_name"]
                else f"- Tool skipped: `{rec['skip_reason']}`"
            ),
            "",
            "### Model turn 1",
            "",
            fence(rec["turn1"]),
        ]
        if rec["tool_result"] is not None:
            lines += [
                "",
                "### Tool result",
                "",
                fence(json.dumps(rec["tool_result"], indent=2)),
            ]
        lines += [
            "",
            "### Final reply",
            "",
            fence(rec["final"] or ""),
            "",
        ]

    TRANSCRIPT_PATH.parent.mkdir(parents=True, exist_ok=True)
    TRANSCRIPT_PATH.write_text("\n".join(lines), encoding="utf-8")
    print(f"Wrote {TRANSCRIPT_PATH}")


if __name__ == "__main__":
    main()
