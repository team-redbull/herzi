#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
python3 - <<'PY'
from pathlib import Path
import yaml
print(yaml.safe_dump(yaml.safe_load(Path('charts/herzi/ci/example-values.yaml').read_text())['config'],sort_keys=False),end='')
PY
