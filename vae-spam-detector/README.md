# VAE Spam Detector

This project implements a ham-only Variational Autoencoder (VAE) spam detector on the UCI SMS Spam Collection dataset. It trains a VAE (plus a regular autoencoder baseline) exclusively on legitimate "ham" messages using TF-IDF features, then flags spam as anomalies via high reconstruction error using a 95th-percentile threshold derived from held-out ham validation data. Performance is compared to a supervised logistic regression baseline, with metrics (accuracy, precision, recall, F1, ROC-AUC, ham FPR, spam recall) and illustrative examples written to `results.json`. The approach demonstrates unsupervised anomaly detection for imbalanced spam filtering without requiring labeled spam during training.

## Quick Start

1. Install dependencies:

   ```bash
   pip install -r requirements.txt
   ```

   > **Note:** PyTorch may require a platform-specific install (CPU, MPS on macOS, or CUDA). Follow the instructions at https://pytorch.org/get-started/locally/ if the default wheel does not match your hardware.

2. Run the detector:

   ```bash
   python vae_spam_detector.py
   ```

The script will download the SMS Spam Collection, train the VAE + autoencoder on ham data only, evaluate all models, print a JSON summary to stdout, and write `results.json` to the project root.
