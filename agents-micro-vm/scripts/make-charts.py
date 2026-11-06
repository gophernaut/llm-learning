#!/usr/bin/env python3
"""Charts for article/post.md, from the benchmark CSVs.

    uv run --with matplotlib scripts/make-charts.py [bare-label] [nested-label]

Defaults: results/i7-6700k-bare-metal (bare metal) and results/m4-lima-nested. Writes
PNGs to article/img/.
"""
import csv
import statistics
import sys
from pathlib import Path

import matplotlib

matplotlib.use("Agg")
import matplotlib.pyplot as plt
from matplotlib.ticker import FuncFormatter, NullFormatter

ROOT = Path(__file__).resolve().parent.parent
BARE = ROOT / "results" / (sys.argv[1] if len(sys.argv) > 1 else "i7-6700k-bare-metal")
NESTED = ROOT / "results" / (sys.argv[2] if len(sys.argv) > 2 else "m4-lima-nested")
OUT = ROOT / "article" / "img"

# Categorical slots 1-4 of the reference palette (validated: adjacent CVD
# dE >= 9.1). Slots 3-4 are under 3:1 on the surface, so every series that
# uses them carries a direct text label.
BLUE, ORANGE, AQUA, YELLOW = "#2a78d6", "#eb6834", "#1baf7a", "#eda100"
SURFACE, INK, INK2, GRID = "#fcfcfb", "#0b0b0b", "#52514e", "#e4e3df"

BARE_NAME = "bare metal (i7-6700K)"
NESTED_NAME = "nested (M4, Lima)"

plt.rcParams.update({
    "figure.facecolor": SURFACE, "axes.facecolor": SURFACE, "savefig.facecolor": SURFACE,
    "font.family": "sans-serif", "font.size": 10,
    "axes.edgecolor": GRID, "axes.labelcolor": INK2, "axes.titlecolor": INK,
    "axes.titlesize": 12, "axes.titleweight": "bold", "axes.titlelocation": "left",
    "axes.spines.top": False, "axes.spines.right": False,
    "axes.grid": True, "grid.color": GRID, "grid.linewidth": 0.8,
    "xtick.color": INK2, "ytick.color": INK2, "xtick.major.size": 0, "ytick.major.size": 0,
    "xtick.minor.size": 0, "ytick.minor.size": 0,
    "legend.frameon": False, "legend.labelcolor": INK,
    "lines.linewidth": 2, "lines.solid_capstyle": "round",
})


def raw(d, sub):
    return list(csv.DictReader(open(d / sub / "raw.csv")))


def summary(d, sub):
    return {(r["phase"], int(r["fork_n"])): r for r in csv.DictReader(open(d / sub / "summary.csv"))}


def lat(rows, phase, n=None):
    return sorted(float(r["latency_ms"]) for r in rows
                  if r["phase"] == phase and (n is None or int(r["fork_n"]) == n))


def ms_fmt(v, _):
    return f"{v / 1000:g} s" if v >= 1000 else f"{v:g} ms"


def caption(fig, text):
    fig.text(0.01, 0.01, text, color=INK2, fontsize=8, ha="left", va="bottom")


def save(fig, name):
    OUT.mkdir(parents=True, exist_ok=True)
    fig.savefig(OUT / name, dpi=200)
    plt.close(fig)
    print("wrote", OUT / name)


HOSTS = ("Bare metal: Intel i7-6700K, host kernel 6.14. Nested: Apple M4 Mac mini, Lima VM, host kernel 6.8.\n"
         "Both: Firecracker v1.17.0, 1 vCPU / 512 MiB guest, block rootfs, 200 runs per line.")


def chart_latency_ecdf():
    fig, ax = plt.subplots(figsize=(7, 4.2))
    for d, color, host in ((BARE, BLUE, "bare metal"), (NESTED, ORANGE, "nested")):
        rows = raw(d, "block")
        for phase, style, label in (("restore", "-", "restore"), ("cold", "--", "cold boot")):
            xs = lat(rows, phase)
            ys = [(i + 1) / len(xs) for i in range(len(xs))]
            ax.step(xs, ys, where="post", color=color, linestyle=style)
            left = phase == "cold" and host == "bare metal"
            y = 0.5 if phase == "restore" else (0.85 if left else 0.7)
            sep = ",\n" if left else ", "
            ax.annotate(f"{label}{sep}{host}\np50 {statistics.median(xs):,.0f} ms", (xs[int(y * len(xs))], y),
                        xytext=(-8 if left else 8, 0), textcoords="offset points",
                        fontsize=8.5, color=INK, va="center", ha="right" if left else "left")
    ax.set_xscale("log")
    ax.xaxis.set_major_formatter(FuncFormatter(ms_fmt))
    ax.xaxis.set_minor_formatter(NullFormatter())
    ax.set_xticks([10, 30, 100, 300, 1000])
    ax.set_xlim(10, 3000)
    ax.set_ylim(0, 1.02)
    ax.yaxis.set_major_formatter(FuncFormatter(lambda v, _: f"{v:.0%}"))
    ax.set_ylabel("share of 200 runs at or below")
    ax.set_xlabel("time until the guest agent answers (log scale)")
    ax.set_title("Restore vs. cold boot, bare metal vs. nested")
    ax.plot([], [], color=BLUE, label="bare metal")
    ax.plot([], [], color=ORANGE, label="nested")
    ax.plot([], [], color=INK2, label="restore")
    ax.plot([], [], color=INK2, linestyle="--", label="cold boot")
    ax.legend(loc="lower left", bbox_to_anchor=(0.32, 0.04), fontsize=8.5)
    fig.subplots_adjust(left=0.11, right=0.97, top=0.91, bottom=0.22)
    caption(fig, HOSTS)
    save(fig, "1-latency-ecdf.png")


def chart_vmm_vs_guest():
    ops = (("Cold boot", "cold"), ("Restore", "restore"), ("Wake from hibernate", "wake"))
    labels, vmm, guest = [], [], []
    for name, phase in ops:
        for d, host in ((BARE, "bare metal"), (NESTED, "nested")):
            s = summary(d, "block")
            total, v = float(s[(phase, 0)]["p50_ms"]), float(s[(phase + "_vmm", 0)]["p50_ms"])
            labels.append(f"{name}\n{host}")
            vmm.append(v)
            guest.append(total - v)
    fig, ax = plt.subplots(figsize=(7, 4.6))
    ys = list(range(len(labels)))[::-1]
    ax.barh(ys, vmm, color=BLUE, height=0.62, label="Firecracker (VMM) part", edgecolor=SURFACE, linewidth=2)
    ax.barh(ys, guest, left=vmm, color=ORANGE, height=0.62, label="guest part", edgecolor=SURFACE, linewidth=2)
    for y, v, g in zip(ys, vmm, guest):
        ax.text(v + g + 12, y, f"{v + g:,.0f} ms  (guest {g / (v + g):.0%})", va="center", fontsize=8.5, color=INK)
    ax.set_yticks(ys, labels, fontsize=8.5, color=INK)
    ax.xaxis.set_major_formatter(FuncFormatter(ms_fmt))
    ax.set_xlim(0, 1400)
    ax.grid(axis="y", visible=False)
    ax.set_xlabel("p50, ms")
    ax.set_title("Where the time goes: Firecracker vs. the guest")
    ax.legend(loc="lower right", fontsize=8.5)
    fig.subplots_adjust(left=0.2, right=0.97, top=0.92, bottom=0.18)
    caption(fig, "p50 of 200 runs (wake: 9). VMM part = until Firecracker's API calls return; guest part = until the agent answers.")
    save(fig, "2-vmm-vs-guest.png")


def chart_fork_scaling():
    fig, ax = plt.subplots(figsize=(7, 4.4))
    for d, color, host in ((BARE, BLUE, "bare metal"), (NESTED, ORANGE, "nested")):
        s = summary(d, "block")
        ns = sorted(n for (p, n) in s if p == "fork")
        full = [float(s[("fork", n)]["p50_ms"]) for n in ns]
        vmm = [float(s[("fork_vmm", n)]["p50_ms"]) for n in ns]
        ax.plot(ns, full, color=color, marker="o", markersize=6, markeredgecolor=SURFACE, markeredgewidth=2)
        ax.plot(ns, vmm, color=color, linestyle="--", marker="o", markersize=5,
                markeredgecolor=SURFACE, markeredgewidth=2, alpha=0.75)
        ax.annotate(f"{host}: ready", (ns[-1], full[-1]), xytext=(8, 0), textcoords="offset points",
                    va="center", fontsize=8.5, color=INK)
        ax.annotate(f"{host}: VMM part", (ns[-1], vmm[-1]), xytext=(8, -12 if host == "nested" else -6),
                    textcoords="offset points", va="center", fontsize=8.5, color=INK2)
    ax.plot([12], [30000], marker="x", markersize=9, color=ORANGE, markeredgewidth=2.5, linestyle="none")
    ax.annotate("nested, N=12:\nnone ready in 30 s", (12, 30000), xytext=(10, 0), textcoords="offset points",
                va="center", fontsize=8.5, color=INK)
    ax.set_xscale("log", base=2)
    ax.set_yscale("log")
    ax.set_xticks([1, 4, 8, 16, 64], ["1", "4", "8", "16", "64"])
    ax.xaxis.set_minor_formatter(NullFormatter())
    ax.yaxis.set_major_formatter(FuncFormatter(ms_fmt))
    ax.yaxis.set_minor_formatter(NullFormatter())
    ax.set_yticks([10, 30, 100, 300, 1000, 3000, 10000, 30000])
    ax.set_xlim(0.8, 400)
    ax.set_ylim(8, 60000)
    ax.set_xlabel("forks started at once (N, log scale)")
    ax.set_ylabel("per-VM time to ready, p50 (log scale)")
    ax.set_title("Fork scaling: a cliff under nesting, a slope on bare metal")
    fig.subplots_adjust(left=0.12, right=0.97, top=0.92, bottom=0.2)
    caption(fig, "3 batches per N. Bare metal: 4 cores / 8 threads. Nested: 6 vCPUs. Solid = until the agent answers; dashed = VMM part.")
    save(fig, "4-fork-scaling.png")


def chart_memory():
    s = summary(BARE, "block")
    ns = sorted(n for (p, n) in s if p == "fork")
    mem = {}
    for r in csv.DictReader(open(BARE / "block" / "memory.csv")):
        mem.setdefault(int(r["fork_n"]), []).append(int(r["memavailable_delta_per_vm_kib"]))
    series = (
        ("RSS", BLUE, [float(s[("fork", n)]["mean_rss_kib"]) / 1024 for n in ns]),
        ("PSS", ORANGE, [float(s[("fork", n)]["mean_pss_kib"]) / 1024 for n in ns]),
        ("MemAvailable drop", YELLOW, [statistics.mean(mem[n]) / 1024 for n in ns]),
        ("private", AQUA, [float(s[("fork", n)]["mean_private_kib"]) / 1024 for n in ns]),
    )
    fig, ax = plt.subplots(figsize=(7, 4.2))
    for name, color, ys in series:
        ax.plot(ns, ys, color=color, marker="o", markersize=6, markeredgecolor=SURFACE, markeredgewidth=2)
        ax.annotate(f"{name} {ys[-1]:.1f} MiB", (ns[-1], ys[-1]), xytext=(8, {"private": -8, "MemAvailable drop": 8}.get(name, 0)),
                    textcoords="offset points", va="center", fontsize=8.5, color=INK)
    ax.set_xscale("log", base=2)
    ax.set_xticks(ns, [str(n) for n in ns])
    ax.xaxis.set_minor_formatter(NullFormatter())
    ax.set_xlim(0.8, 512)
    ax.set_ylim(0, 25)
    ax.yaxis.set_major_formatter(FuncFormatter(lambda v, _: f"{v:g} MiB"))
    ax.set_xlabel("forks of one snapshot running at once (N, log scale)")
    ax.set_ylabel("host memory per VM")
    ax.set_title("Per-VM memory of idle forks of a 512 MiB guest")
    fig.subplots_adjust(left=0.12, right=0.97, top=0.91, bottom=0.23)
    caption(fig, "Bare metal (i7-6700K), block rootfs, N started at once, read 2 s after all are ready, mean of 3 batches.\n"
            "RSS counts shared pages in full for every VM; PSS splits them. At N=1 every page has one user, so all three match.")
    save(fig, "3-memory-per-vm.png")


if __name__ == "__main__":
    chart_latency_ecdf()
    chart_vmm_vs_guest()
    chart_fork_scaling()
    chart_memory()
