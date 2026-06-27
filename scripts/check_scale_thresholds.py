#!/usr/bin/env python3
"""check_scale_thresholds.py — validate Criterion bench results against scale thresholds.

Usage:
    python3 scripts/check_scale_thresholds.py --dry-run
    python3 scripts/check_scale_thresholds.py --results target/criterion/sharded_search_scale/top_k20/100k/estimates.json

Exit codes:
    0  all thresholds pass (or --dry-run)
    1  one or more thresholds breached

Expected Criterion JSON format (per-benchmark `estimates.json`):
    {
      "mean":   { "point_estimate": <ns>, "standard_error": <ns>, ... },
      "median": { "point_estimate": <ns>, ... },
      "slope":  { ... },
      "r_squared": { ... },
      "std_dev":  { "point_estimate": <ns>, ... },
      "typical": { "point_estimate": <ns>, ... }
    }

    All time values are in nanoseconds. Convert to ms: divide by 1_000_000.

Threshold mapping (threshold_key -> Criterion bench group/id):
    sharded_search_p95_100k_ms  ->  sharded_search_scale/top_k20/100k/estimates.json  (median used as p95 proxy)
    sharded_search_p95_1m_ms    ->  sharded_search_scale/top_k20/1m/estimates.json
    sharded_search_p95_10m_ms   ->  sharded_search_scale/top_k20/10m/estimates.json
    fleet_routing_p99_n10_ms    ->  fleet_routing/n10/BulkExec/estimates.json (worst case across task classes)

Run nightly benchmarks with:
    cargo bench --features bench-scale-heavy 2>&1 | tee bench-output.log
    python3 scripts/check_scale_thresholds.py \\
        --results target/criterion/sharded_search_scale/top_k20/100k/estimates.json \\
        --threshold sharded_search_p95_100k_ms
"""

from __future__ import annotations

import argparse
import json
import os
import sys
from pathlib import Path

THRESHOLDS_FILE = Path(__file__).parent.parent / "bench" / "scale-thresholds.json"


def load_thresholds() -> dict:
    """Load and validate the thresholds JSON file."""
    if not THRESHOLDS_FILE.exists():
        sys.exit(f"ERROR: thresholds file not found: {THRESHOLDS_FILE}")
    with THRESHOLDS_FILE.open() as f:
        data = json.load(f)
    if data.get("version") != 1:
        sys.exit(f"ERROR: unsupported thresholds version: {data.get('version')}")
    thresholds = data.get("thresholds")
    if not isinstance(thresholds, dict):
        sys.exit("ERROR: thresholds file missing 'thresholds' dict")
    for key, val in thresholds.items():
        if not isinstance(val, (int, float)):
            sys.exit(f"ERROR: threshold '{key}' must be a number, got {type(val).__name__}")
    return thresholds


def load_criterion_estimates(path: Path) -> float:
    """Extract the median latency (ms) from a Criterion estimates.json file."""
    if not path.exists():
        sys.exit(f"ERROR: estimates file not found: {path}")
    with path.open() as f:
        data = json.load(f)
    # Use median as a p95 proxy. Criterion stores time in nanoseconds.
    median_ns = data["median"]["point_estimate"]
    return median_ns / 1_000_000.0  # ns -> ms


def check_single(results_path: Path, threshold_key: str, thresholds: dict) -> bool:
    """Check one estimates file against one threshold key. Returns True if passing."""
    if threshold_key not in thresholds:
        sys.exit(f"ERROR: unknown threshold key '{threshold_key}'. "
                 f"Valid keys: {list(thresholds.keys())}")
    threshold_ms = thresholds[threshold_key]
    actual_ms = load_criterion_estimates(results_path)
    passed = actual_ms <= threshold_ms
    status = "PASS" if passed else "FAIL"
    print(f"[{status}] {threshold_key}: {actual_ms:.3f} ms (threshold: {threshold_ms} ms)")
    return passed


def dry_run(thresholds: dict) -> None:
    """Validate the thresholds file and print a summary. Always exits 0."""
    print(f"Thresholds file: {THRESHOLDS_FILE}")
    print(f"Version: 1")
    print(f"Thresholds ({len(thresholds)} entries):")
    for key, val in thresholds.items():
        print(f"  {key}: {val} ms")
    print("\n[OK] Thresholds file is valid.")


def main() -> None:
    parser = argparse.ArgumentParser(
        description="Check Criterion bench results against scale-thresholds.json"
    )
    parser.add_argument(
        "--results",
        type=Path,
        help="Path to a Criterion estimates.json file to check.",
    )
    parser.add_argument(
        "--threshold",
        type=str,
        help="Threshold key to check against (from bench/scale-thresholds.json).",
    )
    parser.add_argument(
        "--dry-run",
        action="store_true",
        help="Validate the thresholds file only; do not check any bench results.",
    )
    args = parser.parse_args()

    thresholds = load_thresholds()

    if args.dry_run:
        dry_run(thresholds)
        sys.exit(0)

    if not args.results:
        parser.error("--results is required unless --dry-run is specified")
    if not args.threshold:
        parser.error("--threshold is required unless --dry-run is specified")

    passed = check_single(args.results, args.threshold, thresholds)
    sys.exit(0 if passed else 1)


if __name__ == "__main__":
    main()
