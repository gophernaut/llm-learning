# VAE Spam Detector

This project implements a ham-only VAE / Autoencoder spam detector on the UCI SMS Spam Collection dataset.

**Current best setup (configurable):**
- Sentence embeddings (`all-mpnet-base-v2`) + hand-crafted features (length, digit ratio, money symbols, URLs, etc.)
- All normalization stats computed only on ham training data
- Models trained exclusively on ham
- Threshold selected by maximizing F1 on a small labeled dev set (ham + limited spam) — no fixed percentile
- Strong results: ~207× mean reconstruction error ratio, VAE F1 ≈ 0.87 at ~2% FPR on ham

Performance is compared to a supervised logistic regression baseline. Metrics and examples are written to `results.json`.

The code supports both the modern embeddings path and the original TF-IDF path for comparison. The approach demonstrates how to do practical unsupervised anomaly detection for spam without needing labeled spam during model training.

## Quick Start

1. Install dependencies:

   ```bash
   pip install -r requirements.txt
   ```

   > **Note:** When using embeddings (default), you'll also need `sentence-transformers`. PyTorch may require a platform-specific install (CPU, MPS on macOS, or CUDA).

2. Run the detector:

   ```bash
   python vae_spam_detector.py
   ```

The script will download the SMS Spam Collection, build features (embeddings + hand-crafted by default), train the VAE and autoencoder on ham only, run an architecture sweep (when using embeddings), select the best threshold via max-F1 on a dev set, evaluate, print a JSON summary, and write `results.json`.

To switch representations, edit `FEATURE_MODE` and `PURE_MAX_F1_THRESHOLD` at the top of the script.

## Recent Changes Summary

- Switched primary representation from TF-IDF to sentence embeddings (`all-mpnet-base-v2`) + hand-crafted features (length, digit/money/URL signals, etc.).
- All normalization is strictly ham-training-only.
- Threshold selection changed from fixed 95th-percentile on ham errors to pure max-F1 on a small dev set (ham + limited spam). This gives much better practical F1 with low FPR.
- Added architecture sweep for hidden/latent sizes when using embeddings.
- Decoder/loss adjusted for embeddings (no Sigmoid, MSE).
- Mean reconstruction error ratio improved from ~1.1× to >200×.
- VAE/AE now reach F1 ~0.86–0.88 with ~1–3% FPR on ham (pure max-F1 mode).

See `medium-vae-spam-detector.md` for the full updated write-up and the latest `results.json` for exact numbers.
