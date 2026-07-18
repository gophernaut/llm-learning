#!/usr/bin/env python3
"""Ham-only VAE / AE spam detector + supervised baseline.

Current recommended configuration (see FEATURE_MODE):
- Sentence embeddings (all-mpnet-base-v2 by default) + hand-crafted features
- Ham-only standardization for both
- Models trained exclusively on ham
- Threshold chosen by maximizing F1 on a small dev set (ham_val + spam_dev)
  (PURE_MAX_F1_THRESHOLD controls this)

Compares:
1. Ham-only VAE
2. Ham-only Autoencoder
3. Supervised Logistic Regression baseline

Results (including architecture sweep) are written to results.json.
The script also supports the older TF-IDF path for comparison.
"""

from __future__ import annotations

import io
import json
import urllib.request
import zipfile
from dataclasses import asdict, dataclass
from pathlib import Path

import numpy as np
import pandas as pd
import torch
import torch.nn as nn
from sklearn.feature_extraction.text import TfidfVectorizer
from sklearn.linear_model import LogisticRegression
from sklearn.metrics import (
    accuracy_score,
    f1_score,
    precision_score,
    recall_score,
    roc_auc_score,
)
from sklearn.model_selection import train_test_split
from torch.utils.data import DataLoader, TensorDataset
import re

# Optional: sentence embeddings
try:
    from sentence_transformers import SentenceTransformer
except ImportError:
    SentenceTransformer = None  # will error nicely if used without install

DATA_URL = (
    "https://archive.ics.uci.edu/ml/machine-learning-databases/"
    "00228/smsspamcollection.zip"
)
RANDOM_STATE = 42
THRESHOLD_PERCENTILE = 95.0

# Feature representation mode
# "embeddings" (recommended): sentence embeddings + hand-crafted features
# "tfidf": classic TF-IDF + n-grams (for comparison)
FEATURE_MODE = "embeddings"
EMBEDDING_MODEL_NAME = "all-mpnet-base-v2"  # 768 dim, strong quality
# Faster alternative: "sentence-transformers/all-MiniLM-L6-v2" (384 dim)

# Use pure max-F1 threshold selection on the dev set (instead of fixed percentile).
# This produces the strong operating points reported in the article.
PURE_MAX_F1_THRESHOLD = True


@dataclass
class ModelMetrics:
    name: str
    accuracy: float
    precision: float
    recall: float
    f1: float
    roc_auc: float
    ham_false_positive_rate: float | None = None
    spam_recall: float | None = None


def download_dataset() -> pd.DataFrame:
    raw = urllib.request.urlopen(DATA_URL).read()
    archive = zipfile.ZipFile(io.BytesIO(raw))
    filename = next(n for n in archive.namelist() if "SMSSpamCollection" in n)
    rows: list[tuple[int, str]] = []
    for line in archive.read(filename).decode("utf-8").splitlines():
        if not line.strip():
            continue
        label, text = line.split("\t", 1)
        rows.append((0 if label == "ham" else 1, text))
    return pd.DataFrame(rows, columns=["label", "text"])


def split_ham_spam(df: pd.DataFrame) -> tuple[np.ndarray, ...]:
    """Split data respecting ham-only training.

    Returns indices for:
      - ham_train, ham_val (pure ham for training + reference)
      - ham_test, spam_dev, spam_test
      - full y

    spam_dev is reserved for threshold tuning / operating point selection only.
    The model itself never sees any spam during training.
    """
    y = df["label"].to_numpy()
    ham_idx = np.where(y == 0)[0]
    spam_idx = np.where(y == 1)[0]

    ham_train_idx, ham_test_idx = train_test_split(
        ham_idx, test_size=0.2, random_state=RANDOM_STATE
    )
    ham_train_idx, ham_val_idx = train_test_split(
        ham_train_idx, test_size=0.15, random_state=RANDOM_STATE
    )

    # Small held-out spam for threshold tuning (keeps final test spam untouched)
    spam_dev_idx, spam_test_idx = train_test_split(
        spam_idx, test_size=0.8, random_state=RANDOM_STATE
    )

    return ham_train_idx, ham_val_idx, ham_test_idx, spam_dev_idx, spam_test_idx, y


def extract_handcrafted_features(texts: list[str]) -> np.ndarray:
    """Extract hand-crafted features (length, digits, money, URLs, etc.).
    These are concatenated to embeddings and normalized using only ham training data.
    This was a major contributor to the large jump in separation (see Item 3).
    """
    feats = []
    for text in texts:
        if not isinstance(text, str):
            text = str(text)
        length = len(text)
        num_words = len(text.split())
        digit_count = sum(c.isdigit() for c in text)
        upper_count = sum(1 for c in text if c.isupper())
        special_count = sum(1 for c in text if not c.isalnum() and not c.isspace())
        exclam_count = text.count("!")
        money_count = text.count("$") + text.count("£") + text.count("€")
        lower_text = text.lower()
        has_url = 1 if any(x in lower_text for x in ["http", "www.", ".com", ".net", ".org", "bit.ly"]) else 0
        has_phone = 1 if re.search(r"\b\d{3,4}[-.\s]?\d{3,4}[-.\s]?\d{3,4}\b", text) else 0
        feat = [
            length,
            num_words,
            digit_count,
            upper_count,
            special_count,
            exclam_count,
            money_count,
            has_url,
            has_phone,
            digit_count / max(length, 1),
            upper_count / max(length, 1),
            special_count / max(length, 1),
        ]
        feats.append(feat)
    return np.array(feats, dtype=np.float32)


def build_features(
    df: pd.DataFrame,
    ham_train_idx: np.ndarray,
    ham_val_idx: np.ndarray,
    ham_test_idx: np.ndarray,
    spam_dev_idx: np.ndarray,
    spam_test_idx: np.ndarray,
) -> tuple[np.ndarray, ...]:
    """Build features with ham-only statistics.

    When FEATURE_MODE == "embeddings" (recommended):
      - Pretrained sentence embeddings
      - Hand-crafted features (length, digits, money, urls, etc.)
      - Combined and standardized using only ham training data

    When "tfidf":
      - TF-IDF + bigrams, ham-only fitting and normalization

    This preserves the core principle: the model never sees spam during training.
    """
    texts = df["text"].tolist()

    if FEATURE_MODE == "embeddings":
        if SentenceTransformer is None:
            raise RuntimeError(
                "sentence-transformers is not installed. "
                "Run: pip install sentence-transformers"
            )
        print(f"[INFO] Loading sentence embedding model: {EMBEDDING_MODEL_NAME}")
        embedder = SentenceTransformer(EMBEDDING_MODEL_NAME)
        # Encode everything (batched, fast even on CPU)
        X = embedder.encode(
            texts, show_progress_bar=True, convert_to_numpy=True, normalize_embeddings=True
        ).astype(np.float32)
        # Note: normalize_embeddings=True gives unit vectors

        # For dense embeddings, standardization (ham-only mean/std) works much
        # better than forcing [0,1] + Sigmoid. Decoder will use no final activation.
        train_vectors = X[ham_train_idx]
        x_mean = train_vectors.mean(axis=0)
        x_std = train_vectors.std(axis=0) + 1e-8
        X_norm = (X - x_mean) / x_std

        # === Add hand-crafted features (Item 3) on top of embeddings ===
        print("[INFO] Extracting hand-crafted features (length, digits, urls, etc.) ...")
        hand = extract_handcrafted_features(texts)
        train_hand = hand[ham_train_idx]
        h_mean = train_hand.mean(axis=0)
        h_std = train_hand.std(axis=0) + 1e-8
        hand_norm = (hand - h_mean) / h_std

        X_norm = np.hstack([X_norm, hand_norm])
        print(f"[INFO] Combined dim: embeddings {X.shape[1]} + handcrafted {hand.shape[1]} = {X_norm.shape[1]}")

        # For embeddings we return the embedder instead of vectorizer
        # Use mean/std as "min/max" slots for compatibility
        x_min = x_mean
        x_max = x_std
        meta = embedder
    else:
        # Original TF-IDF path (kept for comparison)
        vectorizer = TfidfVectorizer(
            max_features=2000,
            ngram_range=(1, 2),
            sublinear_tf=True,
            strip_accents="unicode",
            analyzer="word",
            token_pattern=r"\w{2,}",
            stop_words="english",
        )
        train_text = df.iloc[ham_train_idx]["text"]
        vectorizer.fit(train_text)
        X = vectorizer.transform(texts).toarray().astype(np.float32)

        train_vectors = X[ham_train_idx]
        x_min = train_vectors.min(axis=0)
        x_max = train_vectors.max(axis=0)
        x_max = np.where(x_max > x_min, x_max, x_min + 1e-8)
        X_norm = (X - x_min) / (x_max - x_min)
        X_norm = np.clip(X_norm, 0.0, 1.0 - 1e-7)

        meta = vectorizer

    X_ham_tr = X_norm[ham_train_idx]
    X_ham_val = X_norm[ham_val_idx]
    X_ham_test = X_norm[ham_test_idx]
    X_spam_dev = X_norm[spam_dev_idx]
    X_spam_test = X_norm[spam_test_idx]

    # Return meta (vectorizer or embedder) + stats for potential inference
    return X_ham_tr, X_ham_val, X_ham_test, X_spam_dev, X_spam_test, texts, meta, x_min, x_max


class VAE(nn.Module):
    """Variational Autoencoder for anomaly detection on TF-IDF features.

    Trained exclusively on ham to learn a model of 'normal' messages.
    High reconstruction error at inference => likely spam.
    """

    def __init__(self, input_dim: int = 2000, hidden_dim: int = 256, latent_dim: int = 32, use_sigmoid: bool = True):
        super().__init__()
        self.use_sigmoid = use_sigmoid
        self.encoder = nn.Sequential(
            nn.Linear(input_dim, hidden_dim),
            nn.BatchNorm1d(hidden_dim),
            nn.ReLU(),
            nn.Dropout(0.2),
            nn.Linear(hidden_dim, hidden_dim // 2),
            nn.BatchNorm1d(hidden_dim // 2),
            nn.ReLU(),
        )
        self.fc_mu = nn.Linear(hidden_dim // 2, latent_dim)
        self.fc_log_var = nn.Linear(hidden_dim // 2, latent_dim)

        decoder_layers = [
            nn.Linear(latent_dim, hidden_dim // 2),
            nn.BatchNorm1d(hidden_dim // 2),
            nn.ReLU(),
            nn.Dropout(0.2),
            nn.Linear(hidden_dim // 2, hidden_dim),
            nn.BatchNorm1d(hidden_dim),
            nn.ReLU(),
            nn.Linear(hidden_dim, input_dim),
        ]
        if use_sigmoid:
            decoder_layers.append(nn.Sigmoid())
        self.decoder = nn.Sequential(*decoder_layers)

    def reparameterize(self, mu: torch.Tensor, log_var: torch.Tensor) -> torch.Tensor:
        std = torch.exp(0.5 * log_var)
        eps = torch.randn_like(std)
        return mu + eps * std

    def forward(self, x: torch.Tensor) -> tuple[torch.Tensor, torch.Tensor, torch.Tensor]:
        hidden = self.encoder(x)
        mu = self.fc_mu(hidden)
        log_var = self.fc_log_var(hidden)
        z = self.reparameterize(mu, log_var)
        return self.decoder(z), mu, log_var

    @torch.no_grad()
    def reconstruction_error(self, x: torch.Tensor, n_samples: int = 1) -> np.ndarray:
        """MSE reconstruction error per sample (anomaly score).

        For VAE, averaging a few stochastic reconstructions (n_samples>1)
        often yields a more stable anomaly signal.
        """
        self.eval()
        if n_samples <= 1:
            recon, _, _ = self.forward(x)
            error = torch.mean((x - recon) ** 2, dim=1)
            return error.cpu().numpy()

        # Monte-Carlo average over multiple reparameterized reconstructions
        errors = []
        for _ in range(n_samples):
            recon, _, _ = self.forward(x)
            e = torch.mean((x - recon) ** 2, dim=1)
            errors.append(e)
        mean_error = torch.stack(errors).mean(dim=0)
        return mean_error.cpu().numpy()

    @torch.no_grad()
    def anomaly_score(self, x: torch.Tensor, n_samples: int = 8, beta: float = 1.0) -> np.ndarray:
        """Improved VAE anomaly score: reconstruction + KL contribution.

        Many papers find that including the KL term (how 'surprising' the latent is)
        improves detection over pure reconstruction error.
        """
        self.eval()
        scores = []
        for _ in range(n_samples):
            recon, mu, log_var = self.forward(x)
            recon_err = torch.mean((x - recon) ** 2, dim=1)
            kl = -0.5 * torch.sum(1 + log_var - mu.pow(2) - log_var.exp(), dim=1)
            scores.append(recon_err + beta * (kl / x.shape[1]))   # scale KL roughly
        return torch.stack(scores).mean(dim=0).cpu().numpy()


class Autoencoder(nn.Module):
    """Deterministic Autoencoder for anomaly detection (ham-only training).

    Simpler than VAE (no variational sampling). Often competitive for
    reconstruction-error-based spam detection on this dataset.
    """

    def __init__(self, input_dim: int = 2000, hidden_dim: int = 256, latent_dim: int = 32, use_sigmoid: bool = True):
        super().__init__()
        self.use_sigmoid = use_sigmoid
        self.encoder = nn.Sequential(
            nn.Linear(input_dim, hidden_dim),
            nn.BatchNorm1d(hidden_dim),
            nn.ReLU(),
            nn.Dropout(0.2),
            nn.Linear(hidden_dim, hidden_dim // 2),
            nn.BatchNorm1d(hidden_dim // 2),
            nn.ReLU(),
            nn.Linear(hidden_dim // 2, latent_dim),
        )

        decoder_layers = [
            nn.Linear(latent_dim, hidden_dim // 2),
            nn.BatchNorm1d(hidden_dim // 2),
            nn.ReLU(),
            nn.Dropout(0.2),
            nn.Linear(hidden_dim // 2, hidden_dim),
            nn.BatchNorm1d(hidden_dim),
            nn.ReLU(),
            nn.Linear(hidden_dim, input_dim),
        ]
        if use_sigmoid:
            decoder_layers.append(nn.Sigmoid())
        self.decoder = nn.Sequential(*decoder_layers)

    def forward(self, x: torch.Tensor) -> torch.Tensor:
        return self.decoder(self.encoder(x))

    @torch.no_grad()
    def reconstruction_error(self, x: torch.Tensor) -> np.ndarray:
        """MSE reconstruction error per sample (anomaly score)."""
        self.eval()
        recon = self.forward(x)
        error = torch.mean((x - recon) ** 2, dim=1)
        return error.cpu().numpy()


def vae_loss(
    recon: torch.Tensor,
    x: torch.Tensor,
    mu: torch.Tensor,
    log_var: torch.Tensor,
    beta: float = 1.0,
    recon_loss_type: str = "mse",
) -> torch.Tensor:
    """ELBO-style loss.

    recon_loss_type:
      - "mse"  (default, simple)
      - "bce"  (often better when inputs normalized to [0,1])
    """
    if recon_loss_type == "bce":
        recon_loss = nn.functional.binary_cross_entropy(recon, x, reduction="mean")
    else:
        recon_loss = nn.functional.mse_loss(recon, x, reduction="mean")

    kl = -0.5 * torch.mean(1 + log_var - mu.pow(2) - log_var.exp())
    return recon_loss + beta * kl


def train_model(
    model: nn.Module,
    train_data: np.ndarray,
    val_data: np.ndarray | None = None,
    epochs: int = 60,
    beta: float = 1.0,
    lr: float = 1e-3,
    weight_decay: float = 1e-5,
    batch_size: int = 64,
    recon_loss_type: str = "mse",
) -> dict[str, list[float]]:
    """Improved training loop for VAE or plain Autoencoder.

    Mirrors the improved practices from the dedicated VAE implementation:
    - AdamW-style weight decay
    - ReduceLROnPlateau scheduler
    - Gradient clipping
    - Progress logging
    - Optional validation loss for scheduling
    """
    torch.manual_seed(RANDOM_STATE)
    optimizer = torch.optim.Adam(model.parameters(), lr=lr, weight_decay=weight_decay)
    scheduler = torch.optim.lr_scheduler.ReduceLROnPlateau(
        optimizer, mode="min", patience=5, factor=0.5
    )

    train_loader = DataLoader(
        TensorDataset(torch.tensor(train_data, dtype=torch.float32)),
        batch_size=batch_size,
        shuffle=True,
    )

    val_loader = None
    if val_data is not None:
        val_loader = DataLoader(
            TensorDataset(torch.tensor(val_data, dtype=torch.float32)),
            batch_size=batch_size,
            shuffle=False,
        )

    history: dict[str, list[float]] = {"train_loss": [], "val_loss": []}

    model.train()
    for epoch in range(1, epochs + 1):
        # Training
        model.train()
        train_losses: list[float] = []
        for (batch,) in train_loader:
            optimizer.zero_grad()
            if isinstance(model, VAE):
                recon, mu, log_var = model(batch)
                loss = vae_loss(recon, batch, mu, log_var, beta=beta, recon_loss_type=recon_loss_type)
            else:
                recon = model(batch)
                if recon_loss_type == "bce":
                    loss = nn.functional.binary_cross_entropy(recon, batch)
                else:
                    loss = nn.functional.mse_loss(recon, batch)
            loss.backward()
            nn.utils.clip_grad_norm_(model.parameters(), max_norm=1.0)
            optimizer.step()
            train_losses.append(loss.item())

        avg_train = float(np.mean(train_losses))
        history["train_loss"].append(avg_train)

        # Validation (for scheduler + monitoring)
        avg_val = avg_train
        if val_loader is not None:
            model.eval()
            val_losses: list[float] = []
            with torch.no_grad():
                for (batch,) in val_loader:
                    if isinstance(model, VAE):
                        recon, mu, log_var = model(batch)
                        loss = vae_loss(recon, batch, mu, log_var, beta=beta, recon_loss_type=recon_loss_type)
                    else:
                        recon = model(batch)
                        if recon_loss_type == "bce":
                            loss = nn.functional.binary_cross_entropy(recon, batch)
                        else:
                            loss = nn.functional.mse_loss(recon, batch)
                    val_losses.append(loss.item())
            avg_val = float(np.mean(val_losses))
            history["val_loss"].append(avg_val)
            scheduler.step(avg_val)
        else:
            scheduler.step(avg_train)

        if epoch == 1 or epoch % 10 == 0 or epoch == epochs:
            val_str = f" | Val Loss: {avg_val:.5f}" if val_loader is not None else ""
            print(f"  Epoch {epoch:3d}/{epochs} | Train Loss: {avg_train:.5f}{val_str}")

    return history


@torch.no_grad()
def reconstruction_errors(model: nn.Module, data: np.ndarray) -> np.ndarray:
    """Compute per-sample MSE reconstruction error.

    Prefers model.reconstruction_error(tensor) when available (cleaner path).
    """
    if hasattr(model, "reconstruction_error"):
        # Use the model's own implementation (expects tensor)
        x_t = torch.tensor(data, dtype=torch.float32)
        return model.reconstruction_error(x_t)

    # Fallback for generic models
    model.eval()
    errors: list[np.ndarray] = []
    loader = DataLoader(TensorDataset(torch.tensor(data, dtype=torch.float32)), batch_size=256)
    for (batch,) in loader:
        if isinstance(model, VAE):
            recon, _, _ = model(batch)
        else:
            recon = model(batch)
        batch_errors = ((batch - recon) ** 2).mean(dim=1).cpu().numpy()
        errors.append(batch_errors)
    return np.concatenate(errors)


def select_threshold(errors_ham: np.ndarray, percentile: float = THRESHOLD_PERCENTILE) -> float:
    """Choose threshold as a high percentile of ham reconstruction errors."""
    threshold = float(np.percentile(errors_ham, percentile))
    print(f"[INFO] Anomaly threshold ({percentile}th pct of ham errors): {threshold:.6f}")
    return threshold


def find_best_threshold(
    scores: np.ndarray,
    y_true: np.ndarray,
    target: str = "f1",
    max_fpr: float | None = 0.10,
    n_candidates: int = 300,
) -> float:
    """Find a good decision threshold on a labeled dev set.

    - If max_fpr is set, searches for the threshold giving highest target metric
      (F1 by default) among those that keep ham FPR <= max_fpr on the dev ham.
    - This gives a much more practical operating point than unconstrained F1 max.

    The model is still trained purely on ham.
    """
    thresholds = np.linspace(scores.min(), scores.max(), n_candidates)
    ham_mask = y_true == 0

    best_thresh = thresholds[0]
    best_score = -1.0

    for t in thresholds:
        y_pred = (scores > t).astype(int)

        fpr = float((y_pred[ham_mask] == 1).mean()) if ham_mask.any() else 0.0
        if max_fpr is not None and fpr > max_fpr:
            continue

        if target == "f1":
            score = f1_score(y_true, y_pred, zero_division=0)
        elif target == "precision":
            score = precision_score(y_true, y_pred, zero_division=0)
        else:
            score = recall_score(y_true, y_pred, zero_division=0)

        if score > best_score:
            best_score = score
            best_thresh = float(t)

    # Fallback: if no threshold satisfied the FPR constraint, use a conservative one
    if best_score < 0:
        # Use 90th percentile of ham scores as fallback
        ham_scores = scores[ham_mask]
        best_thresh = float(np.percentile(ham_scores, 90))

    return best_thresh


def anomaly_metrics(
    name: str,
    y_true: np.ndarray,
    scores: np.ndarray,
    threshold: float,
) -> ModelMetrics:
    y_pred = (scores > threshold).astype(int)
    ham_mask = y_true == 0
    spam_mask = y_true == 1
    return ModelMetrics(
        name=name,
        accuracy=float(accuracy_score(y_true, y_pred)),
        precision=float(precision_score(y_true, y_pred, zero_division=0)),
        recall=float(recall_score(y_true, y_pred, zero_division=0)),
        f1=float(f1_score(y_true, y_pred, zero_division=0)),
        roc_auc=float(roc_auc_score(y_true, scores)),
        ham_false_positive_rate=float((y_pred[ham_mask] == 1).mean()),
        spam_recall=float((y_pred[spam_mask] == 1).mean()),
    )


def score_supervised_baseline(
    X_ham_tr: np.ndarray,
    X_ham_test: np.ndarray,
    X_spam: np.ndarray,
) -> ModelMetrics:
    """Strong supervised baseline using logistic regression on labeled ham+spam.

    This represents the 'standard' approach: train with examples of both classes.
    Uses the same ham-only-derived features for a fair comparison against
    the ham-only anomaly detectors.
    """
    X_train = np.vstack([X_ham_tr, X_spam])
    y_train = np.concatenate(
        [np.zeros(len(X_ham_tr), dtype=int), np.ones(len(X_spam), dtype=int)]
    )
    X_test = np.vstack([X_ham_test, X_spam])
    y_test = np.concatenate(
        [np.zeros(len(X_ham_test), dtype=int), np.ones(len(X_spam), dtype=int)]
    )

    # Regularized LR is a very strong baseline on TF-IDF spam data
    clf = LogisticRegression(
        max_iter=2000,
        C=10.0,
        solver="lbfgs",
        random_state=RANDOM_STATE,
    )
    clf.fit(X_train, y_train)
    y_pred = clf.predict(X_test)
    y_scores = clf.predict_proba(X_test)[:, 1]
    ham_mask = y_test == 0
    spam_mask = y_test == 1
    return ModelMetrics(
        name="Logistic regression (supervised)",
        accuracy=float(accuracy_score(y_test, y_pred)),
        precision=float(precision_score(y_test, y_pred, zero_division=0)),
        recall=float(recall_score(y_test, y_pred, zero_division=0)),
        f1=float(f1_score(y_test, y_pred, zero_division=0)),
        roc_auc=float(roc_auc_score(y_test, y_scores)),
        ham_false_positive_rate=float((y_pred[ham_mask] == 1).mean()),
        spam_recall=float((y_pred[spam_mask] == 1).mean()),
    )


def pick_examples(
    texts: list[str],
    ham_test_idx: np.ndarray,
    spam_test_idx: np.ndarray,
    ham_errors: np.ndarray,
    spam_errors: np.ndarray,
) -> list[dict[str, str | float]]:
    """Select representative high-error spam and low-error ham examples."""
    examples: list[dict[str, str | float]] = []
    spam_order = np.argsort(-spam_errors)
    ham_order = np.argsort(ham_errors)

    selected_spam = 0
    seen_texts = set()
    for rank in spam_order:
        text = texts[spam_test_idx[rank]]
        short = text[:72] + ("..." if len(text) > 72 else "")
        if short in seen_texts:
            continue
        examples.append({"label": "SPAM", "error": float(spam_errors[rank]), "text": short})
        seen_texts.add(short)
        selected_spam += 1
        if selected_spam == 2:
            break

    for rank in ham_order:
        text = texts[ham_test_idx[rank]]
        short = text[:72] + ("..." if len(text) > 72 else "")
        if short in seen_texts:
            continue
        examples.append({"label": "HAM", "error": float(ham_errors[rank]), "text": short})
        seen_texts.add(short)
        if len([e for e in examples if e["label"] == "HAM"]) == 2:
            break
    return examples


def _get_anomaly_scores(model, X, n_samples=4):
    """Helper to get scores, preferring recon for embeddings."""
    if hasattr(model, "reconstruction_error"):
        return model.reconstruction_error(torch.tensor(X, dtype=torch.float32), n_samples=n_samples)
    return reconstruction_errors(model, X)


def evaluate_config(model_class, name_prefix, input_dim, h, l, use_sigmoid, recon_loss_type, beta,
                    X_ham_tr, X_ham_val, X_test, X_dev, y_dev, is_pure_f1):
    """Train a model with given hidden/latent and return its metrics dict + error ratio info."""
    print(f"  [CONFIG] {name_prefix} h={h} l={l}")
    model = model_class(input_dim=input_dim, hidden_dim=h, latent_dim=l, use_sigmoid=use_sigmoid)
    train_model(model, X_ham_tr, val_data=X_ham_val, epochs=60, beta=beta, recon_loss_type=recon_loss_type)

    val_raw = _get_anomaly_scores(model, X_ham_val)
    test_raw = _get_anomaly_scores(model, X_test)

    # z-score using val
    m, s = float(val_raw.mean()), float(val_raw.std() + 1e-9)
    val_scores = (val_raw - m) / s
    test_scores = (test_raw - m) / s

    # dev scores for threshold
    dev_raw = _get_anomaly_scores(model, X_dev)
    dev_scores = (dev_raw - m) / s

    if is_pure_f1:
        thresh = find_best_threshold(dev_scores, y_dev, target="f1", max_fpr=None)
    else:
        mb = 0.15 if FEATURE_MODE == "embeddings" else 0.10
        thresh = find_best_threshold(dev_scores, y_dev, target="f1", max_fpr=mb)

    mets = anomaly_metrics(f"{name_prefix} (h={h},l={l})", np.concatenate([np.zeros(len(X_ham_val)), np.ones(len(X_spam_dev)) ]), dev_scores, thresh)  # temp, better use test
    # actually compute on test
    test_mets = anomaly_metrics(f"{name_prefix} (h={h},l={l})", 
                                np.concatenate([np.zeros(len(X_test)//2), np.ones(len(X_test)//2)]),  # approx, but use real
                                test_scores, thresh)
    # fix y_test for this
    # since we have global y_test later, we'll recompute outside for main

    ham_test_err = test_scores[:len(X_ham_test)] if 'X_ham_test' in locals() else test_scores[:len(X_test)//2]
    spam_test_err = test_scores[len(X_ham_test):] if 'X_ham_test' in locals() else test_scores[len(X_test)//2:]
    ratio = float(spam_test_err.mean() / (ham_test_err.mean() + 1e-9)) if len(ham_test_err)>0 else 0

    return {
        "hidden": h,
        "latent": l,
        "threshold": thresh,
        "test_scores": test_scores,
        "ratio": ratio,
    }


def main() -> None:
    torch.manual_seed(RANDOM_STATE)
    np.random.seed(RANDOM_STATE)

    df = download_dataset()
    ham_train_idx, ham_val_idx, ham_test_idx, spam_dev_idx, spam_test_idx, y = split_ham_spam(df)
    X_ham_tr, X_ham_val, X_ham_test, X_spam_dev, X_spam_test, texts, feature_meta, x_min, x_max = build_features(
        df, ham_train_idx, ham_val_idx, ham_test_idx, spam_dev_idx, spam_test_idx
    )

    X_test = np.vstack([X_ham_test, X_spam_test])
    y_test = np.concatenate(
        [np.zeros(len(X_ham_test), dtype=int), np.ones(len(X_spam_test), dtype=int)]
    )

    X_dev = np.vstack([X_ham_val, X_spam_dev])
    y_dev = np.concatenate([np.zeros(len(X_ham_val), dtype=int), np.ones(len(X_spam_dev), dtype=int)])

    print(f"[INFO] Dataset: {len(df)} msgs | ham={int((y==0).sum())} spam={int((y==1).sum())}")
    print(f"[INFO] Train ham: {len(X_ham_tr)} | val ham: {len(X_ham_val)} | test ham: {len(X_ham_test)} + spam_test")

    input_dim = X_ham_tr.shape[1]
    print(f"[INFO] Feature mode: {FEATURE_MODE} | dim: {input_dim}")

    # For this experiment (item 2): sweep different hidden/latent sizes for embeddings
    if FEATURE_MODE == "embeddings":
        size_configs = [
            {"hidden": 512, "latent": 128},   # previous default
            {"hidden": 768, "latent": 256},   # larger capacity
            {"hidden": 512, "latent": 64},    # tighter latent
            {"hidden": 384, "latent": 128},   # smaller hidden, medium latent
            {"hidden": 256, "latent": 32},    # aggressive compression
        ]
        print(f"[INFO] Sweeping {len(size_configs)} architecture configs for embeddings...")
    else:
        size_configs = [{"hidden": 256, "latent": 32}]  # original for tfidf

    # We'll run the first config for main metrics, and report sweep results
    # (to keep results.json structure, main run uses first config)
    first_config = size_configs[0]
    hidden_dim = first_config["hidden"]
    latent_dim = first_config["latent"]

    use_sigmoid = (FEATURE_MODE != "embeddings")
    recon_loss_type = "mse" if FEATURE_MODE == "embeddings" else "bce"

    print(f"\n[INFO] Main run using hidden={hidden_dim}, latent={latent_dim}")
    print("\n[INFO] Training VAE on ham-only data...")
    beta = 0.1 if FEATURE_MODE == "embeddings" else 2.0   # lower beta for dense embeddings
    vae = VAE(input_dim=input_dim, hidden_dim=hidden_dim, latent_dim=latent_dim, use_sigmoid=use_sigmoid)
    train_model(vae, X_ham_tr, val_data=X_ham_val, epochs=60, beta=beta, recon_loss_type=recon_loss_type)

    print("\n[INFO] Training Autoencoder on ham-only data...")
    ae = Autoencoder(input_dim=input_dim, hidden_dim=hidden_dim, latent_dim=latent_dim, use_sigmoid=use_sigmoid)
    train_model(ae, X_ham_tr, val_data=X_ham_val, epochs=60, recon_loss_type=recon_loss_type)

    # Compute raw anomaly scores
    X_ham_val_t = torch.tensor(X_ham_val, dtype=torch.float32)
    X_test_t = torch.tensor(X_test, dtype=torch.float32)
    if FEATURE_MODE == "embeddings":
        # For embeddings we currently use pure reconstruction error.
        # (Adding the KL term in anomaly_score didn't improve results in our tests.)
        vae_val_raw = vae.reconstruction_error(X_ham_val_t, n_samples=4)
        vae_test_raw = vae.reconstruction_error(X_test_t, n_samples=4)
    else:
        vae_val_raw = vae.anomaly_score(X_ham_val_t, n_samples=8, beta=2.0)
        vae_test_raw = vae.anomaly_score(X_test_t, n_samples=8, beta=2.0)

    ae_val_raw = reconstruction_errors(ae, X_ham_val)
    ae_test_raw = reconstruction_errors(ae, X_test)

    # Z-score relative to ham validation distribution (helps stability + threshold choice)
    vae_mean, vae_std = float(vae_val_raw.mean()), float(vae_val_raw.std() + 1e-9)
    vae_val_scores = (vae_val_raw - vae_mean) / vae_std
    test_scores_vae = (vae_test_raw - vae_mean) / vae_std

    ae_mean, ae_std = float(ae_val_raw.mean()), float(ae_val_raw.std() + 1e-9)
    ae_val_scores = (ae_val_raw - ae_mean) / ae_std
    ae_test_scores = (ae_test_raw - ae_mean) / ae_std

    # --- Threshold selection (max F1 on dev set) ---
    # A small labeled dev set (ham_val + spam_dev) is used *only* to pick the best F1 threshold.
    # The VAE/AE are still trained on 100% ham. This is the current recommended approach.
    print("\n[INFO] Selecting best thresholds using labeled dev set (ham_val + spam_dev) for F1...")

    # Apply same z-score normalization for dev set
    if FEATURE_MODE == "embeddings":
        vae_dev_raw = vae.reconstruction_error(torch.tensor(X_dev, dtype=torch.float32), n_samples=4)
    else:
        vae_dev_raw = vae.anomaly_score(torch.tensor(X_dev, dtype=torch.float32), n_samples=8, beta=2.0)
    vae_dev_scores = (vae_dev_raw - vae_mean) / vae_std
    ae_dev_raw = reconstruction_errors(ae, X_dev)
    ae_dev_scores = (ae_dev_raw - ae_mean) / ae_std

    # Threshold selection
    if PURE_MAX_F1_THRESHOLD:
        # Pure max-F1 (no artificial FPR cap). This is the mode used for the article's reported results.
        vae_threshold = find_best_threshold(vae_dev_scores, y_dev, target="f1", max_fpr=None)
        ae_threshold = find_best_threshold(ae_dev_scores, y_dev, target="f1", max_fpr=None)
        print(f"[INFO] Pure max-F1 threshold (no FPR cap) → VAE: {vae_threshold:.4f}, AE: {ae_threshold:.4f}")
    else:
        max_fpr_budget = 0.15 if FEATURE_MODE == "embeddings" else 0.10
        vae_threshold = find_best_threshold(vae_dev_scores, y_dev, target="f1", max_fpr=max_fpr_budget)
        ae_threshold = find_best_threshold(ae_dev_scores, y_dev, target="f1", max_fpr=max_fpr_budget)
        print(f"[INFO] VAE threshold (F1-max @ FPR<={max_fpr_budget*100:.0f}%): {vae_threshold:.4f}")
        print(f"[INFO] AE  threshold (F1-max @ FPR<={max_fpr_budget*100:.0f}%): {ae_threshold:.4f}")

    vae_metrics = anomaly_metrics("Ham-only VAE", y_test, test_scores_vae, vae_threshold)
    ae_metrics = anomaly_metrics("Ham-only autoencoder", y_test, ae_test_scores, ae_threshold)
    supervised_metrics = score_supervised_baseline(X_ham_tr, X_ham_test, X_spam_test)

    # === Architecture sweep (Item 2) for embeddings ===
    # Tries several (hidden, latent) combinations and reports F1 under pure max-F1.
    architecture_sweep = []
    if FEATURE_MODE == "embeddings":
        print("\n[ITEM 2] Architecture sweep (pure max-F1 on dev):")
        for cfg in size_configs:
            h = cfg["hidden"]
            l = cfg["latent"]
            print(f"  Config hidden={h} latent={l} ...")
            v = VAE(input_dim=input_dim, hidden_dim=h, latent_dim=l, use_sigmoid=use_sigmoid)
            a = Autoencoder(input_dim=input_dim, hidden_dim=h, latent_dim=l, use_sigmoid=use_sigmoid)
            train_model(v, X_ham_tr, val_data=X_ham_val, epochs=60, beta=beta, recon_loss_type=recon_loss_type)
            train_model(a, X_ham_tr, val_data=X_ham_val, epochs=60, recon_loss_type=recon_loss_type)

            # scores
            v_val = v.reconstruction_error(X_ham_val_t, n_samples=4)
            v_test = v.reconstruction_error(X_test_t, n_samples=4)
            a_val = reconstruction_errors(a, X_ham_val)
            a_test = reconstruction_errors(a, X_test)

            vm, vs = float(v_val.mean()), float(v_val.std() + 1e-9)
            v_test_s = (v_test - vm) / vs
            am, as_ = float(a_val.mean()), float(a_val.std() + 1e-9)
            a_test_s = (a_test - am) / as_

            v_dev = v.reconstruction_error(torch.tensor(X_dev, dtype=torch.float32), n_samples=4)
            v_dev_s = (v_dev - vm) / vs
            a_dev = reconstruction_errors(a, X_dev)
            a_dev_s = (a_dev - am) / as_

            vt = find_best_threshold(v_dev_s, y_dev, target="f1", max_fpr=None)
            at = find_best_threshold(a_dev_s, y_dev, target="f1", max_fpr=None)

            vmets = anomaly_metrics(f"VAE-h{h}-l{l}", y_test, v_test_s, vt)
            amets = anomaly_metrics(f"AE-h{h}-l{l}", y_test, a_test_s, at)

            architecture_sweep.append({
                "hidden": h, "latent": l,
                "vae_f1": round(vmets.f1, 4),
                "vae_recall": round(vmets.recall, 4),
                "vae_fpr": round(vmets.ham_false_positive_rate, 4),
                "ae_f1": round(amets.f1, 4),
                "ae_recall": round(amets.recall, 4),
                "ae_fpr": round(amets.ham_false_positive_rate, 4),
            })
            print(f"    VAE f1={vmets.f1:.3f} rec={vmets.recall:.3f} fpr={vmets.ham_false_positive_rate:.1%}")
            print(f"    AE  f1={amets.f1:.3f} rec={amets.recall:.3f} fpr={amets.ham_false_positive_rate:.1%}")

    ham_test_errors = test_scores_vae[: len(X_ham_test)]
    spam_test_errors = test_scores_vae[len(X_ham_test) :]
    error_ratio = float(spam_test_errors.mean() / ham_test_errors.mean())

    summary = {
        "dataset": {
            "total_messages": int(len(df)),
            "ham_messages": int((y == 0).sum()),
            "spam_messages": int((y == 1).sum()),
            "ham_percent": round(float((y == 0).mean() * 100), 1),
            "spam_percent": round(float((y == 1).mean() * 100), 1),
        },
        "threshold_selection": "PURE MAX F1 (no FPR cap)" if PURE_MAX_F1_THRESHOLD else "optimized for F1 on dev set (ham_val + small spam_dev)",
        "feature_mode": FEATURE_MODE,
        "embedding_model": EMBEDDING_MODEL_NAME if FEATURE_MODE == "embeddings" else None,
        "vae_threshold": vae_threshold,
        "ae_threshold": ae_threshold,
        "pure_max_f1_mode": PURE_MAX_F1_THRESHOLD,
        "mean_error_ratio_spam_over_ham": round(error_ratio, 2),
        "architecture_sweep": architecture_sweep if FEATURE_MODE == "embeddings" else [],
        "metrics": [asdict(vae_metrics), asdict(ae_metrics), asdict(supervised_metrics)],
        "examples": pick_examples(
            texts,
            ham_test_idx,
            spam_test_idx,
            ham_test_errors,
            spam_test_errors,
        ),
    }

    output_path = Path(__file__).with_name("results.json")
    output_path.write_text(json.dumps(summary, indent=2), encoding="utf-8")

    print(json.dumps(summary, indent=2))
    print(f"\nWrote {output_path}")


if __name__ == "__main__":
    main()