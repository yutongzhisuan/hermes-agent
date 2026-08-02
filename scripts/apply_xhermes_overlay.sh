#!/usr/bin/env bash
# 在干净上游树上重放 xhermes 改名。合并上游后运行：
#   git merge upstream/main && scripts/apply_xhermes_overlay.sh
set -euo pipefail

# 单点常量已在 hermes_constants.py；此脚本兜底散落字面量。
# 平台判断：macOS 用 sed -i ''，Linux 用 sed -i。
if [[ "$(uname -s)" == "Darwin" ]]; then
  SED_INLINE=(sed -i '')
else
  SED_INLINE=(sed -i)
fi

echo "→ apply_xhermes_overlay: re-asserting fork naming on $(uname -s)"

# 1. 命令名：shutil.which("hermes") → "xhermes"
rg -l 'shutil\.which\("hermes"\)' --glob '*.py' --glob '!tests/**' 2>/dev/null | \
  while read -r f; do
    "${SED_INLINE[@]}" 's/shutil\.which("hermes")/shutil.which("xhermes")/g' "$f"
    echo "  patched: $f"
  done || true

# 2. launchd / systemd label（只改非注释行，跳过中性名 svc-reload-tmp）
rg -l 'ai\.hermes\.' --glob '*.py' --glob '!tests/**' 2>/dev/null | \
  while read -r f; do
    "${SED_INLINE[@]}" '/^[[:space:]]*#/! s/ai\.hermes\./ai.xhermes./g' "$f"
    echo "  patched: $f"
  done || true

# 3. 服务基名：上游 hermes-gateway → xhermes-gateway
rg -l '_SERVICE_BASE = "xhermes-gateway"|SERVICE_NAME = "xhermes-gateway"' --glob '*.py' --glob '*.sh' 2>/dev/null | \
  while read -r f; do
    "${SED_INLINE[@]}" 's/_SERVICE_BASE = "xhermes-gateway"/_SERVICE_BASE = "xhermes-gateway"/g; s/SERVICE_NAME = "xhermes-gateway"/SERVICE_NAME = "xhermes-gateway"/g' "$f"
    echo "  patched: $f"
  done || true

# 4. 默认家目录 fallback（只改非注释行；跳过 hermes_constants.py 的 legacy 检测清单）
#    已知限制：多行注释/字符串的延续行（行首无 #）无法被 sed 地址排除，可能被误改。
#    运行后请人工 review `git diff`，将注释/测试脚本中的误改还原。
rg -l 'Path\.home\(\)\s*/\s*"\.hermes"' --glob '*.py' --glob '!tests/**' 2>/dev/null | \
  while read -r f; do
    if [[ "$f" == "hermes_constants.py" ]]; then
      echo "  skip (legacy detection list): $f"
      continue
    fi
    "${SED_INLINE[@]}" '/^[[:space:]]*#/! s|Path\.home() / "\.hermes"|Path.home() / ".xhermes"|g' "$f"
    echo "  patched: $f"
  done || true

# 5. 端口偏移（若上游改回）
rg -l '= 9119|port = 9119' --glob '*.py' --glob '!tests/**' 2>/dev/null | \
  while read -r f; do
    "${SED_INLINE[@]}" 's/= 9119/= 9219/g; s/port = 9119/port = 9219/g' "$f"
    echo "  patched: $f"
  done || true

echo "→ Overlay applied. Run merge checklist (§5.4 of design doc):"
rg -n 'shutil\.which\("hermes"\)|_SERVICE_BASE = "hermes|ai\.hermes\.' --glob '!tests/**' 2>/dev/null || echo "  clean"
