#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
promtool check rules charts/herzi/files/alerts.rules.yaml
promtool test rules hack/chart/alerts.test.yaml
