"""Local support agent: SmolLM3 + one order-lookup tool.

Saves a markdown transcript of the tool-call loop.

Run from the repo root (venv activated):

    python Article_1/code/support_agent.py
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
TRANSCRIPT_PATH = ROOT / "transcripts" / "support_agent.md"

TOOLS = [
    {
        "name": "lookup_order_status",
        "description": (
            "Look up the current status, estimated delivery date, and carrier "
            "for a specific customer order. Call this when the customer mentions "
            "an order number or asks where their order is."
        ),
        "parameters": {
            "type": "object",
            "properties": {
                "order_id": {
                    "type": "string",
                    "description": "The order ID, usually in the format ORD-XXXXXX.",
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


def lookup_order_status(order_id: str) -> dict:
    return ORDERS.get(order_id, {"status": "not_found", "eta": None, "carrier": None})


def parse_tool_call(output: str):
    match = re.search(r"<tool_call>(.*?)</tool_call>", output, flags=re.DOTALL)
    if not match:
        return None, None
    try:
        payload = json.loads(match.group(1).strip())
        return payload.get("name"), payload.get("arguments", {})
    except json.JSONDecodeError:
        return None, None


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
    tool_name, tool_args = parse_tool_call(turn1)

    record = {
        "user": user_message,
        "turn1": turn1.strip(),
        "tool_name": tool_name,
        "tool_args": tool_args,
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
        record["final"] = generate_reply(model, tokenizer, messages).strip()
    else:
        record["final"] = turn1.strip()

    record["seconds"] = time.perf_counter() - t0
    return record


def fence(text: str) -> str:
    return f"```\n{text.rstrip()}\n```"


def main() -> None:
    print(f"Loading {MODEL_ID}...")
    model, tokenizer = load(MODEL_ID)

    example_prompt = tokenizer.apply_chat_template(
        [{"role": "user", "content": QUERIES[0]}],
        xml_tools=TOOLS,
        enable_thinking=False,
        add_generation_prompt=True,
        tokenize=False,
    )

    records = []
    for query in QUERIES:
        print(f"Customer: {query}")
        record = respond_with_tools(model, tokenizer, query)
        records.append(record)
        if record["tool_name"]:
            print(f"  tool: {record['tool_name']}({record['tool_args']}) -> {record['tool_result']}")
        else:
            print("  tool: (none)")
        print(f"  {record['seconds']:.1f}s")

    lines = [
        "# Support agent transcript",
        "",
        f"- Model: `{MODEL_ID}`",
        f"- Thinking: `enable_thinking=False`",
        f"- Tool: `lookup_order_status`",
        "",
        "## Rendered prompt for the first query (chat template + xml_tools)",
        "",
        fence(example_prompt),
        "",
    ]

    for i, rec in enumerate(records, start=1):
        lines += [
            f"## Query {i}",
            "",
            f"**Customer:** {rec['user']}",
            "",
            f"- Seconds: {rec['seconds']:.1f}",
            f"- Tool called: `{rec['tool_name']}`" if rec["tool_name"] else "- Tool called: none",
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
                "",
                "### Final reply",
                "",
                fence(rec["final"]),
            ]
        else:
            lines += [
                "",
                "### Final reply (no tool)",
                "",
                fence(rec["final"]),
            ]
        lines.append("")

    TRANSCRIPT_PATH.parent.mkdir(parents=True, exist_ok=True)
    TRANSCRIPT_PATH.write_text("\n".join(lines), encoding="utf-8")
    print(f"Wrote {TRANSCRIPT_PATH}")


if __name__ == "__main__":
    main()
