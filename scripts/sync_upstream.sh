#!/usr/bin/env bash
# 拉取 Nous 上游并重放 xhermes 改名 overlay。
# 日常更新请用 `xhermes update`（吃 fork）；本脚本用于定期人工同步上游修复。
set -euo pipefail

echo "→ sync_upstream: fetching NousResearch/xhermes-agent"
git fetch upstream

echo "→ merging upstream/main"
git merge upstream/main

echo "→ re-applying xhermes rename overlay"
scripts/apply_xhermes_overlay.sh

echo ""
echo "完成。下一步："
echo "  1. scripts/run_tests.sh -q        # 基线测试"
echo "  2. ./scripts/xhermes_coexist_smoke.sh  # 双实例共存 smoke"
echo "  3. git add -A && git commit -m \"chore: sync upstream + re-apply xhermes overlay\""
