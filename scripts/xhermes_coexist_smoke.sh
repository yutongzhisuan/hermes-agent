#!/usr/bin/env bash
# 验证 xhermes 与 hermes 同机共存（§8.3 验收）
# 前置：同机已装 hermes（~/.hermes 存在、hermes 命令可用）；xhermes 已装独立 venv。
set -uo pipefail

FAIL=0
check() {
  local desc="$1"; shift
  if "$@" >/dev/null 2>&1; then
    echo "  ✅ $desc"
  else
    echo "  ❌ $desc"
    FAIL=1
  fi
}

echo "1. 版本各自正常"
check "xhermes --version" xhermes --version
check "hermes --version" hermes --version

echo "2. 家目录隔离"
XH=$("$HOME/.xhermes/xhermes-agent/venv/bin/python" -c "from hermes_constants import get_hermes_home; print(get_hermes_home())" 2>/dev/null)
check "get_hermes_home() = ~/.xhermes (got: $XH)" test "$XH" = "$HOME/.xhermes"
check "~/.hermes 存在（hermes 数据未被破坏）" test -d "$HOME/.hermes"
check "~/.xhermes 存在" test -d "$HOME/.xhermes"

echo "3. 不修改对方家目录（启动 xhermes 后 hermes home mtime 不变）"
HERMES_MTIME=$(stat -f%m "$HOME/.hermes" 2>/dev/null || echo "missing")
"$HOME/.xhermes/xhermes-agent/venv/bin/python" -c "from hermes_constants import get_hermes_home" 2>/dev/null
HERMES_MTIME2=$(stat -f%m "$HOME/.hermes" 2>/dev/null || echo "missing")
check "hermes home 未被 rename/删除" test "$HERMES_MTIME" = "$HERMES_MTIME2"

echo "4. profile wrapper 指向 xhermes"
"$HOME/.xhermes/xhermes-agent/venv/bin/xhermes" profile create smoke-probe >/dev/null 2>&1 || true
check "wrapper 在 ~/.xhermes/bin" test -f "$HOME/.xhermes/bin/smoke-probe"
check "wrapper 内容调用 xhermes" grep -q "xhermes" "$HOME/.xhermes/bin/smoke-probe" 2>/dev/null
rm -f "$HOME/.xhermes/bin/smoke-probe"

echo "5. 端口不冲突（默认值错开）"
check "xhermes 默认端口 9219（hermes 9119）" \
  grep -q "9219" "$HOME/.xhermes/xhermes-agent/hermes_cli/web_server.py" 2>/dev/null

echo "6. XHERMES_HOME 优先于 HERMES_HOME"
OUT=$(HERMES_HOME="$HOME/.hermes" XHERMES_HOME="$HOME/.xhermes" \
  "$HOME/.xhermes/xhermes-agent/venv/bin/python" -c \
  "from hermes_constants import get_hermes_home; print(get_hermes_home())" 2>/dev/null)
check "XHERMES_HOME 胜出 (got: $OUT)" test "$OUT" = "$HOME/.xhermes"

echo ""
if [ "$FAIL" -eq 0 ]; then
  echo "SMOKE PASS"
else
  echo "SMOKE FAIL"
  exit 1
fi
