#!/usr/bin/env bash
# Runs the promtool unit tests against the alert rules that SHIP in config/prometheus.
# promtool reads native rule groups, not the PrometheusRule wrapper, so the groups are extracted into a
# temporary directory first; the shipped file is never modified.
set -euo pipefail
cd "$(dirname "$0")/.."
command -v promtool >/dev/null || { echo "test-alerts: promtool is required (https://prometheus.io/download/)" >&2; exit 1; }
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
python3 - "$work" <<'PY'
import sys, yaml
from pathlib import Path
work = Path(sys.argv[1])
doc = yaml.safe_load(Path("config/prometheus/herzi-alerts.yaml").read_text())
(work / "herzi-alerts.rules.yaml").write_text(yaml.safe_dump({"groups": doc["spec"]["groups"]}))
PY
cp config/prometheus/herzi-alerts.test.yaml "$work/"
cd "$work"
promtool check rules herzi-alerts.rules.yaml
promtool test rules herzi-alerts.test.yaml
