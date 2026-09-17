"""QLoRA SFT of SmolLM3-3B on short GO-UNITTEST-BENCH rows.

Canonical path is a CUDA GPU (Colab T4 or better). Apple Silicon can run
`--profile smoke` only as a pipeline check; 4-bit bitsandbytes on MPS is
not the recipe this article measures.
"""

from __future__ import annotations

import argparse
import gc
import inspect
import json
import sys
from pathlib import Path

import torch
from peft import LoraConfig
from transformers import AutoModelForCausalLM, AutoTokenizer, BitsAndBytesConfig
from trl import SFTConfig, SFTTrainer

sys.path.insert(0, str(Path(__file__).resolve().parent))
from prepare_dataset import MODEL_ID, SHORT_CHARS, load_practice_splits  # noqa: E402

PROFILES = {
    "smoke": {
        "max_train": 8,
        "max_steps": 2,
        "max_length": 512,
        "epochs": 1,
        "grad_accum": 2,
        "save_strategy": "no",
    },
    "practice": {
        "max_train": 256,
        "max_steps": -1,
        "max_length": 2048,
        "epochs": 2,
        "grad_accum": 8,
        "save_strategy": "epoch",
    },
    "full_short": {
        "max_train": None,
        "max_steps": -1,
        "max_length": 2048,
        "epochs": 3,
        "grad_accum": 8,
        "save_strategy": "epoch",
    },
}


def pick_compute():
    if torch.cuda.is_available():
        bf16 = torch.cuda.is_bf16_supported()
        return {
            "name": "cuda",
            "dtype": torch.bfloat16 if bf16 else torch.float16,
            "bf16": bf16,
            "fp16": not bf16,
            "use_qlora": True,
            "pin_memory": True,
        }
    if torch.backends.mps.is_available():
        return {
            "name": "mps",
            "dtype": torch.float16,
            "bf16": False,
            "fp16": False,
            "use_qlora": False,
            "pin_memory": False,
        }
    return {
        "name": "cpu",
        "dtype": torch.float32,
        "bf16": False,
        "fp16": False,
        "use_qlora": False,
        "pin_memory": False,
    }


def load_model(compute: dict):
    tokenizer = AutoTokenizer.from_pretrained(MODEL_ID)
    if tokenizer.pad_token is None:
        tokenizer.pad_token = tokenizer.eos_token
    tokenizer.padding_side = "right"

    if compute["use_qlora"]:
        bnb = BitsAndBytesConfig(
            load_in_4bit=True,
            bnb_4bit_compute_dtype=compute["dtype"],
            bnb_4bit_use_double_quant=True,
            bnb_4bit_quant_type="nf4",
        )
        model = AutoModelForCausalLM.from_pretrained(
            MODEL_ID,
            quantization_config=bnb,
            device_map="auto",
        )
    else:
        # Full-precision LoRA. Fits a smoke run on 24GB unified memory; not
        # the Colab recipe.
        model = AutoModelForCausalLM.from_pretrained(
            MODEL_ID,
            dtype=compute["dtype"],
            device_map=None,
        )
        if compute["name"] == "mps":
            model = model.to("mps")
    model.config.use_cache = False
    return tokenizer, model


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--profile", choices=PROFILES, default="practice")
    parser.add_argument(
        "--output-dir",
        type=Path,
        default=Path(__file__).resolve().parents[1] / "outputs" / "smollm3-go-sft",
    )
    args = parser.parse_args()
    profile = PROFILES[args.profile]
    compute = pick_compute()
    print("compute", compute)
    print("profile", args.profile, profile)

    splits = load_practice_splits(max_chars=SHORT_CHARS)
    train = splits["train"].shuffle(seed=42)
    if profile["max_train"] is not None:
        train = train.select(range(min(profile["max_train"], len(train))))
    # Drop helper columns so SFTTrainer sees prompt/completion.
    keep = ["prompt", "completion", "chat_template_kwargs"]
    train = train.remove_columns([c for c in train.column_names if c not in keep])
    print("train rows", len(train))

    tokenizer, model = load_model(compute)
    lora = LoraConfig(
        r=16,
        lora_alpha=32,
        lora_dropout=0.05,
        bias="none",
        task_type="CAUSAL_LM",
        target_modules=[
            "q_proj",
            "k_proj",
            "v_proj",
            "o_proj",
            "gate_proj",
            "up_proj",
            "down_proj",
        ],
    )
    adapter_dir = args.output_dir / "adapter"
    args.output_dir.mkdir(parents=True, exist_ok=True)

    sft_kwargs = dict(
        output_dir=str(args.output_dir),
        num_train_epochs=profile["epochs"],
        max_steps=profile["max_steps"],
        per_device_train_batch_size=1,
        gradient_accumulation_steps=profile["grad_accum"],
        learning_rate=2e-4,
        lr_scheduler_type="cosine",
        bf16=compute["bf16"],
        fp16=compute["fp16"],
        logging_steps=1 if args.profile == "smoke" else 5,
        save_strategy=profile["save_strategy"],
        gradient_checkpointing=True,
        gradient_checkpointing_kwargs={"use_reentrant": False},
        dataloader_pin_memory=compute["pin_memory"],
        report_to="none",
    )
    sft_fields = inspect.signature(SFTConfig).parameters
    if "max_length" in sft_fields:
        sft_kwargs["max_length"] = profile["max_length"]
    elif "max_seq_length" in sft_fields:
        sft_kwargs["max_seq_length"] = profile["max_length"]
    if "completion_only_loss" in sft_fields:
        sft_kwargs["completion_only_loss"] = True
    sft = SFTConfig(**sft_kwargs)

    trainer_fields = inspect.signature(SFTTrainer.__init__).parameters
    trainer_kwargs = dict(
        model=model,
        train_dataset=train,
        args=sft,
        peft_config=lora,
    )
    if "processing_class" in trainer_fields:
        trainer_kwargs["processing_class"] = tokenizer
    else:
        trainer_kwargs["tokenizer"] = tokenizer
    trainer = SFTTrainer(**trainer_kwargs)
    trainer.model.print_trainable_parameters()
    result = trainer.train()
    trainer.save_model(str(adapter_dir))
    tokenizer.save_pretrained(str(adapter_dir))
    metrics = dict(result.metrics)
    metrics["profile"] = args.profile
    metrics["compute"] = compute["name"]
    metrics["train_rows"] = len(train)
    (args.output_dir / "train_metrics.json").write_text(
        json.dumps(metrics, indent=2, default=str), encoding="utf-8"
    )
    print("saved adapter", adapter_dir)
    print("metrics", metrics)

    del trainer
    del model
    gc.collect()
    if torch.cuda.is_available():
        torch.cuda.empty_cache()
    elif torch.backends.mps.is_available():
        torch.mps.empty_cache()


if __name__ == "__main__":
    main()
