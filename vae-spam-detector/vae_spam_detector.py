#!/usr/bin/env python3
"""Ham-only VAE spam detector on the UCI SMS Spam Collection dataset."""

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

DATA_URL = (
    "https://archive.ics.uci.edu/ml/machine-learning-databases/"
    "00228/smsspamcollection.zip"
)
RANDOM_STATE = 42
THRESHOLD_PERCENTILE = 95.0


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
    y = df["label"].to_numpy()
    ham_idx = np.where(y == 0)[0]
    spam_idx = np.where(y == 1)[0]

    ham_train_idx, ham_test_idx = train_test_split(
        ham_idx, test_size=0.2, random_state=RANDOM_STATE
    )
    ham_train_idx, ham_val_idx = train_test_split(
        ham_train_idx, test_size=0.15, random_state=RANDOM_STATE
    )
    return ham_train_idx, ham_val_idx, ham_test_idx, spam_idx, y


def build_features(
    df: pd.DataFrame,
    ham_train_idx: np.ndarray,
    ham_val_idx: np.ndarray,
    ham_test_idx: np.ndarray,
    spam_idx: np.ndarray,
) -> tuple[np.ndarray, np.ndarray, np.ndarray, np.ndarray, list[str]]:
    vectorizer = TfidfVectorizer(
        max_features=1000,
        sublinear_tf=True,
        strip_accents="unicode",
        analyzer="word",
        token_pattern=r"\w{2,}",
        stop_words="english",
    )
    train_text = df.iloc[ham_train_idx]["text"]
    vectorizer.fit(train_text)
    texts = df["text"].tolist()
    X = vectorizer.transform(texts).toarray().astype(np.float32)

    train_vectors = X[ham_train_idx]
    x_min = train_vectors.min()
    x_max = train_vectors.max()
    X_norm = (X - x_min) / (x_max - x_min + 1e-8)

    X_ham_tr = X_norm[ham_train_idx]
    X_ham_val = X_norm[ham_val_idx]
    X_ham_test = X_norm[ham_test_idx]
    X_spam = X_norm[spam_idx]
    return X_ham_tr, X_ham_val, X_ham_test, X_spam, texts


class VAE(nn.Module):
    def __init__(self, input_dim: int = 1000, hidden_dim: int = 256, latent_dim: int = 32):
        super().__init__()
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
        self.decoder = nn.Sequential(
            nn.Linear(latent_dim, hidden_dim // 2),
            nn.BatchNorm1d(hidden_dim // 2),
            nn.ReLU(),
            nn.Dropout(0.2),
            nn.Linear(hidden_dim // 2, hidden_dim),
            nn.BatchNorm1d(hidden_dim),
            nn.ReLU(),
            nn.Linear(hidden_dim, input_dim),
            nn.Sigmoid(),
        )

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


class Autoencoder(nn.Module):
    def __init__(self, input_dim: int = 1000, hidden_dim: int = 256, latent_dim: int = 32):
        super().__init__()
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
        self.decoder = nn.Sequential(
            nn.Linear(latent_dim, hidden_dim // 2),
            nn.BatchNorm1d(hidden_dim // 2),
            nn.ReLU(),
            nn.Dropout(0.2),
            nn.Linear(hidden_dim // 2, hidden_dim),
            nn.BatchNorm1d(hidden_dim),
            nn.ReLU(),
            nn.Linear(hidden_dim, input_dim),
            nn.Sigmoid(),
        )

    def forward(self, x: torch.Tensor) -> torch.Tensor:
        return self.decoder(self.encoder(x))


def vae_loss(
    recon: torch.Tensor,
    x: torch.Tensor,
    mu: torch.Tensor,
    log_var: torch.Tensor,
    beta: float = 1.0,
) -> torch.Tensor:
    recon_loss = nn.functional.mse_loss(recon, x, reduction="sum") / x.size(0)
    kl = -0.5 * torch.sum(1 + log_var - mu.pow(2) - log_var.exp()) / x.size(0)
    return recon_loss + beta * kl


def train_vae(
    model: nn.Module,
    train_data: np.ndarray,
    epochs: int = 60,
    beta: float = 1.0,
) -> None:
    torch.manual_seed(RANDOM_STATE)
    optimizer = torch.optim.Adam(model.parameters(), lr=1e-3)
    loader = DataLoader(
        TensorDataset(torch.tensor(train_data)),
        batch_size=64,
        shuffle=True,
    )

    model.train()
    for _ in range(epochs):
        for (batch,) in loader:
            optimizer.zero_grad()
            if isinstance(model, VAE):
                recon, mu, log_var = model(batch)
                loss = vae_loss(recon, batch, mu, log_var, beta=beta)
            else:
                recon = model(batch)
                loss = nn.functional.mse_loss(recon, batch)
            loss.backward()
            nn.utils.clip_grad_norm_(model.parameters(), max_norm=1.0)
            optimizer.step()


@torch.no_grad()
def reconstruction_errors(model: nn.Module, data: np.ndarray) -> np.ndarray:
    model.eval()
    errors: list[np.ndarray] = []
    loader = DataLoader(TensorDataset(torch.tensor(data)), batch_size=256)
    for (batch,) in loader:
        if isinstance(model, VAE):
            recon, _, _ = model(batch)
        else:
            recon = model(batch)
        batch_errors = ((batch - recon) ** 2).mean(dim=1).cpu().numpy()
        errors.append(batch_errors)
    return np.concatenate(errors)


def select_threshold(errors_ham: np.ndarray, percentile: float = THRESHOLD_PERCENTILE) -> float:
    return float(np.percentile(errors_ham, percentile))


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
    X_train = np.vstack([X_ham_tr, X_spam])
    y_train = np.concatenate(
        [np.zeros(len(X_ham_tr), dtype=int), np.ones(len(X_spam), dtype=int)]
    )
    X_test = np.vstack([X_ham_test, X_spam])
    y_test = np.concatenate(
        [np.zeros(len(X_ham_test), dtype=int), np.ones(len(X_spam), dtype=int)]
    )

    clf = LogisticRegression(max_iter=1000, random_state=RANDOM_STATE)
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
    spam_idx: np.ndarray,
    ham_errors: np.ndarray,
    spam_errors: np.ndarray,
) -> list[dict[str, str | float]]:
    examples: list[dict[str, str | float]] = []
    spam_order = np.argsort(-spam_errors)
    ham_order = np.argsort(ham_errors)

    for rank in spam_order[:2]:
        text = texts[spam_idx[rank]]
        examples.append(
            {
                "label": "SPAM",
                "error": float(spam_errors[rank]),
                "text": text[:72] + ("..." if len(text) > 72 else ""),
            }
        )
    for rank in ham_order[:2]:
        text = texts[ham_test_idx[rank]]
        examples.append(
            {
                "label": "HAM",
                "error": float(ham_errors[rank]),
                "text": text[:72] + ("..." if len(text) > 72 else ""),
            }
        )
    return examples


def main() -> None:
    df = download_dataset()
    ham_train_idx, ham_val_idx, ham_test_idx, spam_idx, y = split_ham_spam(df)
    X_ham_tr, X_ham_val, X_ham_test, X_spam, texts = build_features(
        df, ham_train_idx, ham_val_idx, ham_test_idx, spam_idx
    )

    X_test = np.vstack([X_ham_test, X_spam])
    y_test = np.concatenate(
        [np.zeros(len(X_ham_test), dtype=int), np.ones(len(X_spam), dtype=int)]
    )

    vae = VAE()
    train_vae(vae, X_ham_tr, epochs=60, beta=1.0)
    val_errors = reconstruction_errors(vae, X_ham_val)
    test_errors = reconstruction_errors(vae, X_test)
    threshold = select_threshold(val_errors)

    ae = Autoencoder()
    train_vae(ae, X_ham_tr, epochs=60)
    ae_test_errors = reconstruction_errors(ae, X_test)
    ae_threshold = select_threshold(reconstruction_errors(ae, X_ham_val))

    vae_metrics = anomaly_metrics("Ham-only VAE", y_test, test_errors, threshold)
    ae_metrics = anomaly_metrics("Ham-only autoencoder", y_test, ae_test_errors, ae_threshold)
    supervised_metrics = score_supervised_baseline(X_ham_tr, X_ham_test, X_spam)

    ham_test_errors = test_errors[: len(X_ham_test)]
    spam_test_errors = test_errors[len(X_ham_test) :]
    error_ratio = float(spam_test_errors.mean() / ham_test_errors.mean())

    summary = {
        "dataset": {
            "total_messages": int(len(df)),
            "ham_messages": int((y == 0).sum()),
            "spam_messages": int((y == 1).sum()),
            "ham_percent": round(float((y == 0).mean() * 100), 1),
            "spam_percent": round(float((y == 1).mean() * 100), 1),
        },
        "threshold_percentile": THRESHOLD_PERCENTILE,
        "threshold_value": threshold,
        "mean_error_ratio_spam_over_ham": round(error_ratio, 2),
        "metrics": [asdict(vae_metrics), asdict(ae_metrics), asdict(supervised_metrics)],
        "examples": pick_examples(
            texts,
            ham_test_idx,
            spam_idx,
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