from __future__ import annotations

import json
from pathlib import Path

OUT = Path(__file__).resolve().parents[1] / "colab_smollm3_go_sft.ipynb"


def md(source: str) -> dict:
    return {"cell_type": "markdown", "metadata": {}, "source": source.strip("\n") + "\n"}


def code(source: str) -> dict:
    return {
        "cell_type": "code",
        "metadata": {},
        "execution_count": None,
        "outputs": [],
        "source": source.strip("\n") + "\n",
    }


cells = [
    md(
        """
# SmolLM3 → Go unit tests (QLoRA practice lab)

Fine-tune `HuggingFaceTB/SmolLM3-3B` to emit a Go test file for a source file, then score with `go test`.

**Runtime → Change runtime type → T4 GPU** (or L4 / A100). CPU will not finish. Apple Silicon is not this notebook; use the `.py` scripts in `smollm3-go-finetuning-for-tests-generation/code/` for dataset inspection and the base-model baseline only.

Profiles:

| `PROFILE` | What it does | Typical T4 time |
|---|---|---|
| `smoke` | 8 rows, 2 optimizer steps | 3–8 min, pipeline check |
| `practice` | 256 short rows, 2 epochs | ~25–45 min, the article recipe |
| `full_short` | all short rows (`code+test <= 6000` chars), 3 epochs | 1–2 h |
"""
    ),
    code(
        """
# 1) GPU must be visible before we spend 20 minutes installing nothing useful.
import torch

print("cuda", torch.cuda.is_available())
if torch.cuda.is_available():
    print(torch.cuda.get_device_name(0))
    print("bf16", torch.cuda.is_bf16_supported())
    print("vram_gb", round(torch.cuda.get_device_properties(0).total_memory / 1024**3, 1))
else:
    raise SystemExit("No CUDA GPU. Runtime → Change runtime type → T4 GPU, then Run all.")
"""
    ),
    code(
        """
# 2) Libraries. SmolLM3 needs transformers >= 4.53. torchao is a transformers
# import-time dependency on recent Colab images — without it, cell 9's reload
# fails before you get to see the after-eval scoreboard.
%pip -q install -U "transformers>=4.53.0" "trl>=0.21.0" "peft>=0.15.0" "bitsandbytes>=0.45.0" "datasets>=2.19.0" "torchao>=0.18.0" accelerate
"""
    ),
    code(
        """
# 3) Go toolchain for the only metric that matters.
import shutil, subprocess, sys

if shutil.which("go") is None:
    subprocess.check_call(["apt-get", "-qq", "update"])
    subprocess.check_call(["apt-get", "-qq", "install", "-y", "golang-go"])
print(subprocess.check_output(["go", "version"], text=True).strip())
"""
    ),
    code(
        r'''
# 4) Config. Change PROFILE here, then Run all.
from __future__ import annotations

import gc, inspect, json, os, re, subprocess, tempfile, time
from pathlib import Path

import torch
from datasets import load_dataset
from peft import LoraConfig, PeftModel
from transformers import AutoModelForCausalLM, AutoTokenizer, BitsAndBytesConfig
from trl import SFTConfig, SFTTrainer

os.environ["WANDB_DISABLED"] = "true"

MODEL_ID = "HuggingFaceTB/SmolLM3-3B"
CODE_COL = "Code"
TEST_COL = "Unit Test - (Ground Truth)"
SHORT_CHARS = 6000
OUTPUT_DIR = Path("/content/smollm3-go-sft")
ADAPTER_DIR = OUTPUT_DIR / "adapter"
PROFILE = "full_short"  # smoke | practice | full_short

PROFILES = {
    "smoke": dict(max_train=8, max_steps=2, max_length=512, epochs=1, grad_accum=2, save_strategy="no"),
    "practice": dict(max_train=256, max_steps=-1, max_length=2048, epochs=2, grad_accum=8, save_strategy="epoch"),
    "full_short": dict(max_train=None, max_steps=-1, max_length=2048, epochs=3, grad_accum=8, save_strategy="epoch"),
}
assert PROFILE in PROFILES, PROFILE
CFG = PROFILES[PROFILE]
print("PROFILE", PROFILE, CFG)

BF16 = torch.cuda.is_bf16_supported()
DTYPE = torch.bfloat16 if BF16 else torch.float16
print("compute dtype", DTYPE)

EVAL_CASES = {
    "iotest_writer": dict(split="test", index=986, module="example.com/iotest", source_name="writer.go", test_name="writer_test.go"),
    "slowmessage": dict(split="test", index=898, module="example.com/slowmessage", source_name="slowmessage.go", test_name="slowmessage_test.go"),
    # Found with code/find_eval_cases.py: short, stdlib-only imports, verified
    # to `go test` PASS standalone before ever touching the model.
    "k8s_allocator_utils": dict(split="test", index=22, module="example.com/case22", source_name="utils.go", test_name="utils_test.go"),
    "stdlib_errors": dict(split="test", index=119, module="example.com/case119", source_name="errors.go", test_name="errors_test.go"),
    "stdlib_http_mapping": dict(split="test", index=201, module="example.com/case201", source_name="mapping.go", test_name="mapping_test.go"),
    "stdlib_cover_html": dict(split="test", index=258, module="example.com/case258", source_name="html.go", test_name="html_test.go"),
    "stdlib_zstd_window": dict(split="test", index=341, module="example.com/case341", source_name="window.go", test_name="window_test.go"),
    "k8s_mux_discovery": dict(split="test", index=390, module="example.com/case390", source_name="mux_discovery_complete.go", test_name="mux_discovery_complete_test.go"),
    "stdlib_testing_allocs": dict(split="test", index=449, module="example.com/case449", source_name="allocs.go", test_name="allocs_test.go"),
    "k8s_webhook_resolver": dict(split="test", index=506, module="example.com/case506", source_name="serviceresolver.go", test_name="serviceresolver_test.go"),
    "k8s_configz": dict(split="test", index=611, module="example.com/case611", source_name="configz.go", test_name="configz_test.go"),
    "k8s_valuefuzz": dict(split="test", index=683, module="example.com/case683", source_name="valuefuzz.go", test_name="valuefuzz_test.go"),
    "tidb_hack": dict(split="test", index=705, module="example.com/case705", source_name="hack.go", test_name="hack_test.go"),
    "gin_bytesconv": dict(split="test", index=738, module="example.com/case738", source_name="bytesconv.go", test_name="bytesconv_test.go"),
}

PACKAGE_RE = re.compile(r"^package\s+\w+", re.MULTILINE)
THINK_RE = re.compile(r"<think>.*?</think>", re.DOTALL)


def format_example(example):
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
        "completion": [{"role": "assistant", "content": example[TEST_COL]}],
        "chat_template_kwargs": {"enable_thinking": False},
    }


def extract_go(text: str) -> str:
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


def run_go_test(source: str, test_src: str, source_name: str, test_name: str, module: str) -> dict:
    with tempfile.TemporaryDirectory() as tmp:
        root = Path(tmp)
        (root / source_name).write_text(source)
        (root / test_name).write_text(test_src)
        subprocess.run(["go", "mod", "init", module], cwd=root, capture_output=True, text=True)
        p = subprocess.run(["go", "test", "-count=1", "."], cwd=root, capture_output=True, text=True)
        return {"passed": p.returncode == 0, "stdout": p.stdout, "stderr": p.stderr}


def generate_test(model, tokenizer, row, max_new_tokens=1024):
    user = (
        f"Write a Go unit test file for `{row['File Name']}` "
        f"({row['File path in Repository']}).\n\n"
        f"```go\n{row[CODE_COL]}\n```"
    )
    messages = [
        {"role": "system", "content": "/no_think"},
        {"role": "user", "content": user},
    ]
    prompt = tokenizer.apply_chat_template(
        messages, tokenize=False, add_generation_prompt=True, enable_thinking=False
    )
    inputs = tokenizer(prompt, return_tensors="pt")
    inputs = {k: v.to(model.device) for k, v in inputs.items()}
    t0 = time.perf_counter()
    with torch.no_grad():
        out = model.generate(
            **inputs, max_new_tokens=max_new_tokens, temperature=0.2, top_p=0.9, do_sample=True
        )
    elapsed = time.perf_counter() - t0
    raw = tokenizer.decode(out[0][inputs["input_ids"].shape[-1] :], skip_special_tokens=True)
    go = extract_go(raw)
    return {"raw": raw, "go": go, "seconds": elapsed}
'''
    ),
    code(
        """
# 5) Inspect the dataset. This cell is the landmine section of the article.
raw = load_dataset("Nutanix/GO-UNITTEST-BENCH")
train = raw["train"]
print("splits", {k: len(v) for k, v in raw.items()})
print("columns", train.column_names)

code_lens = sorted(len(r[CODE_COL]) for r in train)
test_lens = sorted(len(r[TEST_COL]) for r in train)

def pct(xs, p):
    return xs[min(len(xs) - 1, int(round((p / 100) * (len(xs) - 1))))]

print("code chars p50/p90/p99/max", pct(code_lens, 50), pct(code_lens, 90), pct(code_lens, 99), code_lens[-1])
print("test chars p50/p90/p99/max", pct(test_lens, 50), pct(test_lens, 90), pct(test_lens, 99), test_lens[-1])

giant = max(train, key=lambda r: len(r[CODE_COL]))
print("giant row", giant["Repository"], giant["File Name"], len(giant[CODE_COL]), "chars")
print(giant[CODE_COL][:180].replace("\\n", " / "))

tok_probe = AutoTokenizer.from_pretrained(MODEL_ID)
print("tokenizer.model_max_length", tok_probe.model_max_length)
print("short train rows", sum(1 for r in train if len(r[CODE_COL]) + len(r[TEST_COL]) <= SHORT_CHARS))
"""
    ),
    code(
        """
# 6) Practice split: drop the 7.2M-char parser and everything else that will not fit.
def keep_short(ex):
    return bool(ex[CODE_COL]) and bool(ex[TEST_COL]) and (
        len(ex[CODE_COL]) + len(ex[TEST_COL]) <= SHORT_CHARS
    )

splits = {}
for name, rows in raw.items():
    n = len(rows)
    rows = rows.filter(keep_short).map(format_example)
    print(f"{name}: {n} -> {len(rows)}")
    splits[name] = rows

train_sft = splits["train"].shuffle(seed=42)
if CFG["max_train"] is not None:
    train_sft = train_sft.select(range(min(CFG["max_train"], len(train_sft))))
train_sft = train_sft.remove_columns(
    [c for c in train_sft.column_names if c not in {"prompt", "completion", "chat_template_kwargs"}]
)
print("SFT rows", len(train_sft))
"""
    ),
    code(
        """
# 7) Baseline: base SmolLM3 vs `go test` on two held-out standalone files.
# Ground-truth tests already pass; the question is whether the base model emits a file that compiles.

def load_base():
    tokenizer = AutoTokenizer.from_pretrained(MODEL_ID)
    model = AutoModelForCausalLM.from_pretrained(
        MODEL_ID, dtype=DTYPE, device_map="auto"
    )
    model.eval()
    return tokenizer, model


def eval_cases(model, tokenizer, tag: str):
    records = []
    for name, spec in EVAL_CASES.items():
        ds = load_dataset("Nutanix/GO-UNITTEST-BENCH", split=spec["split"])
        row = ds[spec["index"]]
        gt = run_go_test(row[CODE_COL], row[TEST_COL], spec["source_name"], spec["test_name"], spec["module"])
        gen = generate_test(model, tokenizer, row)
        pred = run_go_test(row[CODE_COL], gen["go"], spec["source_name"], spec["test_name"], spec["module"])
        rec = {
            "case": name,
            "tag": tag,
            "gt_pass": gt["passed"],
            "gen_pass": pred["passed"],
            "seconds": round(gen["seconds"], 1),
            "stderr": (pred["stderr"] or "")[-800:],
            "go": gen["go"],
            "raw": gen["raw"],
        }
        records.append(rec)
        print(f"{tag} {name}: gt={'PASS' if rec['gt_pass'] else 'FAIL'} gen={'PASS' if rec['gen_pass'] else 'FAIL'} {rec['seconds']}s")
        if not rec["gen_pass"]:
            print(rec["stderr"])
            print("--- extracted head ---")
            print(rec["go"][:500])
            print("----------------------")
    out = Path("/content") / f"eval_{tag}.json"
    out.write_text(json.dumps(records, indent=2))
    print("wrote", out)
    return records

tokenizer, base_model = load_base()
baseline = eval_cases(base_model, tokenizer, "baseline")
del base_model
gc.collect()
torch.cuda.empty_cache()
"""
    ),
    code(
        """
# 8) QLoRA SFT. 4-bit base + LoRA on attention and SwiGLU projections.
# Do not merge the adapter into the 4-bit weights in-place.

tokenizer = AutoTokenizer.from_pretrained(MODEL_ID)
if tokenizer.pad_token is None:
    tokenizer.pad_token = tokenizer.eos_token
tokenizer.padding_side = "right"

bnb = BitsAndBytesConfig(
    load_in_4bit=True,
    bnb_4bit_compute_dtype=DTYPE,
    bnb_4bit_use_double_quant=True,
    bnb_4bit_quant_type="nf4",
)
model = AutoModelForCausalLM.from_pretrained(
    MODEL_ID, quantization_config=bnb, device_map={"": 0}
)
model.config.use_cache = False

lora = LoraConfig(
    r=16,
    lora_alpha=32,
    lora_dropout=0.05,
    bias="none",
    task_type="CAUSAL_LM",
    target_modules=["q_proj", "k_proj", "v_proj", "o_proj", "gate_proj", "up_proj", "down_proj"],
)

sft_kwargs = dict(
    output_dir=str(OUTPUT_DIR),
    num_train_epochs=CFG["epochs"],
    max_steps=CFG["max_steps"],
    per_device_train_batch_size=1,
    gradient_accumulation_steps=CFG["grad_accum"],
    learning_rate=2e-4,
    lr_scheduler_type="cosine",
    bf16=BF16,
    fp16=not BF16,
    logging_steps=1 if PROFILE == "smoke" else 5,
    save_strategy=CFG["save_strategy"],
    gradient_checkpointing=True,
    gradient_checkpointing_kwargs={"use_reentrant": False},
    report_to="none",
)
fields = inspect.signature(SFTConfig).parameters
if "max_length" in fields:
    sft_kwargs["max_length"] = CFG["max_length"]
elif "max_seq_length" in fields:
    sft_kwargs["max_seq_length"] = CFG["max_length"]
if "completion_only_loss" in fields:
    sft_kwargs["completion_only_loss"] = True

trainer_kwargs = dict(
    model=model,
    train_dataset=train_sft,
    args=SFTConfig(**sft_kwargs),
    peft_config=lora,
)
tfields = inspect.signature(SFTTrainer.__init__).parameters
if "processing_class" in tfields:
    trainer_kwargs["processing_class"] = tokenizer
else:
    trainer_kwargs["tokenizer"] = tokenizer

trainer = SFTTrainer(**trainer_kwargs)
trainer.model.print_trainable_parameters()
result = trainer.train()
OUTPUT_DIR.mkdir(parents=True, exist_ok=True)
trainer.save_model(str(ADAPTER_DIR))
tokenizer.save_pretrained(str(ADAPTER_DIR))
(OUTPUT_DIR / "train_metrics.json").write_text(json.dumps(result.metrics, indent=2, default=str))
print(result.metrics)

del trainer, model
gc.collect()
torch.cuda.empty_cache()
"""
    ),
    code(
        """
# 9) After: reload fp16/bf16 base + adapter. Do not generate from the 4-bit training object.
tokenizer = AutoTokenizer.from_pretrained(str(ADAPTER_DIR))
base = AutoModelForCausalLM.from_pretrained(MODEL_ID, dtype=DTYPE, device_map="auto")
tuned = PeftModel.from_pretrained(base, str(ADAPTER_DIR))
tuned.eval()
after = eval_cases(tuned, tokenizer, "after")

print("\\n=== scoreboard ===")
for b, a in zip(baseline, after):
    print(f"{b['case']}: baseline gen={'PASS' if b['gen_pass'] else 'FAIL'} -> after gen={'PASS' if a['gen_pass'] else 'FAIL'}")
"""
    ),
    code(
        """
# 10) Persist the adapter (small) and a paste-back summary for the article.
from google.colab import files

summary = {
    "profile": PROFILE,
    "gpu": torch.cuda.get_device_name(0),
    "train_rows": len(train_sft),
    "baseline": [{k: r[k] for k in ("case", "gt_pass", "gen_pass", "seconds")} for r in baseline],
    "after": [{k: r[k] for k in ("case", "gt_pass", "gen_pass", "seconds")} for r in after],
}
Path("/content/colab_results.json").write_text(json.dumps(summary, indent=2))
print(json.dumps(summary, indent=2))
print("adapter dir", ADAPTER_DIR, "files", list(ADAPTER_DIR.glob('*')))

# Uncomment to download the adapter zip from the browser.
# !zip -r /content/smollm3-go-adapter.zip /content/smollm3-go-sft/adapter
# files.download("/content/smollm3-go-adapter.zip")
"""
    ),
]

nb = {
    "nbformat": 4,
    "nbformat_minor": 5,
    "metadata": {
        "kernelspec": {"display_name": "Python 3", "language": "python", "name": "python3"},
        "language_info": {"name": "python", "pygments_lexer": "ipython3"},
        "accelerator": "GPU",
    },
    "cells": cells,
}
OUT.write_text(json.dumps(nb, indent=2), encoding="utf-8")
print("wrote", OUT)
