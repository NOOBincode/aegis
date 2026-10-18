#!/usr/bin/env bash
# scripts/check-skills.sh —— 开发期 Skill 污染检查（只读）。
# 覆盖：
#   1. skills-lock.json 可解析；已锁定 skill 目录齐全；没有未登记的第三方 skill 目录
#   2. 禁止 symlink / 可执行资产（防 skill 夹带可执行代码）
#   3. 禁止 SKILL.md 中的 curl|sh / wget|sh / base64|sh 安装型注入
#   4. lock 的 computedHash 无法用 SKILL.md 原文复现时给出 warning（需要 skill 管理器
#      的 canonicalization 才能做强校验；默认不 fail，--strict-lock 时 fail）
# 用法：check-skills.sh [--strict-lock]
set -euo pipefail

AEGIS_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
export AEGIS_ROOT
SCRIPT_NAME="check-skills"
# shellcheck source=lib/common.sh
. "${AEGIS_ROOT}/scripts/lib/common.sh"
trap_common_err

STRICT=0
[ "${1:-}" = "--strict-lock" ] && STRICT=1
FAIL=0

cd "$AEGIS_ROOT"
[ -f skills-lock.json ] || die 2 preflight missing_lock "缺 skills-lock.json"

python3 - "$STRICT" <<'PYEOF'
import hashlib, json, os, re, sys
strict = sys.argv[1] == "1"
lock = json.load(open("skills-lock.json"))["skills"]
root = ".agents/skills"
dirs = {d for d in os.listdir(root) if os.path.isdir(os.path.join(root, d))}
local = {d for d in dirs if d.startswith("aegis-")}
missing = sorted(set(lock) - dirs)
extra = sorted(dirs - set(lock) - local)
bad_hash = []
for name, meta in lock.items():
    p = os.path.join(root, name, "SKILL.md")
    h = hashlib.sha256(open(p, "rb").read()).hexdigest()
    if h != meta.get("computedHash"):
        bad_hash.append(name)
print(f"locked={len(lock)} local={len(local)} missing={len(missing)} extra={len(extra)} hash_mismatch={len(bad_hash)}")
if missing:
    print("MISSING:", ",".join(missing)); sys.exit(1)
if extra:
    print("EXTRA:", ",".join(extra)); sys.exit(1)
if bad_hash:
    print("HASH_WARN: lock computedHash 与 SKILL.md 原文 SHA256 不一致；可能是 skill 管理器 canonicalization 差异，需确认：", ",".join(bad_hash[:5]))
    if strict:
        sys.exit(1)
PYEOF
if [ $? -ne 0 ]; then FAIL=1; fi

# symlink / 可执行资产
symlinks="$(find .agents/skills -type l -print 2>/dev/null || true)"
if [ -n "$symlinks" ]; then
  fail skills "reason=symlink" "paths=$(echo "$symlinks" | tr '\n' ' ')"; FAIL=1
else
  info skills "symlinks=none"
fi
execs="$(find .agents/skills -type f -perm -111 -print 2>/dev/null || true)"
if [ -n "$execs" ]; then
  fail skills "reason=executable_asset" "paths=$(echo "$execs" | tr '\n' ' ')"; FAIL=1
else
  info skills "executable_assets=none"
fi
# 安装型管道注入（只在 SKILL.md 指令面检查；示例代码目录不算）
risky="$(grep -rInE '(curl|wget|base64 -d)[^|]*\|\s*(sh|bash)' .agents/skills --include='SKILL.md' 2>/dev/null || true)"
if [ -n "$risky" ]; then
  fail skills "reason=install_pipe_injection" "hits=$(echo "$risky" | wc -l)"; FAIL=1
else
  info skills "install_pipe_patterns=none"
fi

if [ "$FAIL" -ne 0 ]; then log error summary fail; exit 1; fi
log info summary pass
